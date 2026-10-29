package ratel

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/jackc/pgx/v5"
)

// plan is what a built value sends: its statements in order, rendered when
// the value is bound. A value that cannot render keeps its error, which every
// verb and db.Batch return before anything is sent.
type plan struct {
	// head is the first statement and rest the others, so the common single
	// statement needs no slice.
	head    stmt
	rest    []stmt
	err     error
	missing refusal // returned when the statements affect or return no row
}

// multiPlan is a plan of statements rendered together, or of their error.
func multiPlan(rendered []stmt, err error) plan {
	if len(rendered) == 0 {
		return plan{err: err}
	}

	return plan{head: rendered[0], rest: rendered[1:], err: err}
}

func (p plan) count() int { return 1 + len(p.rest) }

func (p plan) at(index int) stmt {
	if index == 0 {
		return p.head
	}

	return p.rest[index-1]
}

// execPlan is a plan whose result is an affected-row count.
type execPlan struct {
	plan
	target writeTarget
}

// rowsPlan is a plan whose result is rows scanned by the table's scanner.
// A write plan maps integrity violations to *ConstraintError.
type rowsPlan[M any] struct {
	plan
	table Table[M]
	write bool
}

// ExecQuery is a built value that is run for its affected-row count: a write
// without Returning, an insert, or a Command. It is the item kind of Exec in
// db.Batch.
type ExecQuery interface{ execPlan() execPlan }

// RowQuery is a built value that is run for its rows: a SELECT, a static
// row query, or a write with Returning. It is the item kind of List and One
// in db.Batch.
type RowQuery[M any] interface{ rowsPlan() rowsPlan[M] }

// ExecRunner is a built value bound to a database; its only verb is Exec.
type ExecRunner struct {
	db   *DB
	plan execPlan
}

// Exec runs the statements and returns the total affected rows. On failure it
// returns zero, even if an earlier statement committed: chunked inserts are not
// atomic, so use a caller-owned transaction when that matters. A write that
// matches no row returns its refusal (ErrNotFound, ErrStaleVersion).
func (r ExecRunner) Exec(ctx context.Context, options ...CallOption) (int64, error) {
	if r.db == nil {
		return 0, ErrInvalidQuery
	}

	if r.plan.err != nil {
		return 0, r.plan.err
	}

	var total int64

	for index := range r.plan.count() {
		rendered := r.plan.at(index)

		tag, err := r.db.executor.Exec(ctx, rendered.SQL, r.db.arguments(options, rendered.Args)...)
		if err != nil {
			return 0, mapWriteError(err, r.plan.target)
		}

		total += tag.RowsAffected()
	}

	if total == 0 && r.plan.missing != "" {
		return 0, r.plan.missing
	}

	return total, nil
}

// RowRunner is a built row query bound to a database. It runs the statements
// verbatim, so DML with RETURNING works: One and First add no LIMIT and stop
// reading after two rows and one row, still running every statement.
type RowRunner[M any] struct {
	db   *DB
	plan rowsPlan[M]
}

// stopped ends a run early, without error, when an iterator consumer breaks.
const stopped refusal = "ratel: stopped"

func (r RowRunner[M]) fail(err error, phase string) error {
	if r.plan.write {
		return mapWriteError(err, r.plan.table.target())
	}

	return fmt.Errorf("ratel: %s: %w", phase, err)
}

// run sends each statement and hands its rows to visit, closing the cursor
// before the next statement starts.
func (r RowRunner[M]) run(ctx context.Context, options []CallOption, visit func(pgx.Rows) error) error {
	if r.db == nil {
		return ErrInvalidQuery
	}

	if r.plan.err != nil {
		return r.plan.err
	}

	for index := range r.plan.count() {
		rendered := r.plan.at(index)

		rows, err := r.db.executor.Query(ctx, rendered.SQL, r.db.arguments(options, rendered.Args)...)
		if err != nil {
			return r.fail(err, "query")
		}

		err = visit(rows)

		rows.Close()

		if err != nil {
			if errors.Is(err, stopped) {
				return nil
			}

			return err
		}

		if err = rows.Err(); err != nil {
			return r.fail(err, "iterate")
		}
	}

	return nil
}

func (r RowRunner[M]) scan(rows pgx.Rows) (M, error) {
	model, err := r.plan.table.scan(rows)
	if err != nil {
		return model, r.fail(err, "scan")
	}

	return model, nil
}

// List collects every row; an empty result is a non-nil empty slice, or the
// write's refusal. An error returns no partial result.
func (r RowRunner[M]) List(ctx context.Context, options ...CallOption) ([]M, error) {
	models := make([]M, 0)

	err := r.run(ctx, options, func(rows pgx.Rows) error {
		for rows.Next() {
			model, err := r.scan(rows)
			if err != nil {
				return err
			}

			models = append(models, model)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(models) == 0 && r.plan.missing != "" {
		return nil, r.plan.missing
	}

	return models, nil
}

// All lazily streams rows. Breaking iteration releases the cursor; an error
// is yielded once with a zero model.
func (r RowRunner[M]) All(ctx context.Context, options ...CallOption) iter.Seq2[M, error] {
	return func(yield func(M, error) bool) {
		var zero M

		yielded := false

		err := r.run(ctx, options, func(rows pgx.Rows) error {
			for rows.Next() {
				model, err := r.scan(rows)
				if err != nil {
					return err
				}

				yielded = true

				if !yield(model, nil) {
					return stopped
				}
			}

			return nil
		})

		switch {
		case err != nil:
			yield(zero, err)
		case !yielded && r.plan.missing != "":
			yield(zero, r.plan.missing)
		default:
		}
	}
}

// One requires exactly one row: ErrNotFound (or the write's refusal) for none,
// ErrMultipleRows for more.
func (r RowRunner[M]) One(ctx context.Context, options ...CallOption) (M, error) {
	const detectMultiple = 2

	return r.single(ctx, detectMultiple, options)
}

// First returns the first row or ErrNotFound (or the write's refusal).
func (r RowRunner[M]) First(ctx context.Context, options ...CallOption) (M, error) {
	return r.single(ctx, 1, options)
}

func (r RowRunner[M]) single(ctx context.Context, limit int, options []CallOption) (M, error) {
	var model, zero M

	found := 0

	err := r.run(ctx, options, func(rows pgx.Rows) error {
		for found < limit && rows.Next() {
			if found == 0 {
				first, err := r.scan(rows)
				if err != nil {
					return err
				}

				model = first
			}

			found++
		}

		return nil
	})

	switch {
	case err != nil:
		return zero, err
	case found == 0 && r.plan.missing != "":
		return zero, r.plan.missing
	case found == 0:
		return zero, ErrNotFound
	case found > 1:
		return zero, ErrMultipleRows
	default:
		return model, nil
	}
}
