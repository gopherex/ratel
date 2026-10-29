package ratel

import (
	"context"
	"fmt"
	"iter"

	"github.com/jackc/pgx/v5"
)

// All lazily streams rows. Breaking iteration always releases the cursor.
// Errors are yielded once with a zero model; no further rows follow an error.
func (q Select[M]) All(ctx context.Context, options ...CallOption) iter.Seq2[M, error] {
	return func(yield func(M, error) bool) {
		q.iterate(ctx, options, yield)
	}
}

func (q Select[M]) iterate(ctx context.Context, options []CallOption, yield func(M, error) bool) {
	var zero M

	if q.db == nil {
		yield(zero, ErrInvalidQuery)

		return
	}

	rows, err := q.rows(ctx, options)
	if err != nil {
		yield(zero, err)

		return
	}

	defer rows.Close()

	for rows.Next() {
		model, scanErr := q.source.table.scan(rows)
		if scanErr != nil {
			yield(zero, fmt.Errorf("ratel: scan SELECT: %w", scanErr))

			return
		}

		if !yield(model, nil) {
			return
		}
	}

	if err = rows.Err(); err != nil {
		yield(zero, fmt.Errorf("ratel: iterate SELECT: %w", err))
	}
}

func (q Select[M]) rows(ctx context.Context, options []CallOption) (pgx.Rows, error) {
	statement, err := q.statement()
	if err != nil {
		return nil, err
	}

	rows, err := q.db.executor.Query(ctx, statement.SQL, q.db.arguments(options, statement.Args)...)
	if err != nil {
		return nil, fmt.Errorf("ratel: query SELECT: %w", err)
	}

	return rows, nil
}

// List collects the result. Empty results are a non-nil empty slice.
// An error returns no partial result.
func (q Select[M]) List(ctx context.Context, options ...CallOption) ([]M, error) {
	return q.collect(ctx, 0, options)
}

// collect reads the result; a positive atMost stops after that many rows.
func (q Select[M]) collect(ctx context.Context, atMost int, options []CallOption) ([]M, error) {
	if q.db == nil {
		return nil, ErrInvalidQuery
	}

	rows, err := q.rows(ctx, options)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	models := make([]M, 0, q.capacity(atMost))

	for rows.Next() {
		model, err := q.source.table.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("ratel: scan SELECT: %w", err)
		}

		models = append(models, model)
		if len(models) == atMost {
			break
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ratel: iterate SELECT: %w", err)
	}

	return models, nil
}

// preallocatedRows bounds the result capacity reserved from a LIMIT, so a
// large limit on a small result does not reserve memory it never fills.
const preallocatedRows = 1024

// capacity is the room to reserve for the result: the row bound the query
// states, up to preallocatedRows.
func (q Select[M]) capacity(atMost int) int {
	bound := atMost
	if q.limit.Set && (bound <= 0 || q.limit.V < bound) {
		bound = q.limit.V
	}

	return max(0, min(bound, preallocatedRows))
}

// One requires exactly one row. It returns ErrNotFound or ErrMultipleRows otherwise.
// At most two rows are requested, without increasing an existing lower limit.
func (q Select[M]) One(ctx context.Context, options ...CallOption) (M, error) {
	const detectMultiple = 2

	return q.single(ctx, detectMultiple, options)
}

// First returns the first row in query order, or ErrNotFound. Without OrderBy,
// PostgreSQL chooses the row; callers needing determinism must supply an order.
func (q Select[M]) First(ctx context.Context, options ...CallOption) (M, error) {
	return q.single(ctx, 1, options)
}

// single reads the first row, then, when limit allows a second, checks that
// there is none.
func (q Select[M]) single(ctx context.Context, limit int, options []CallOption) (M, error) {
	var zero M

	if q.db == nil {
		return zero, ErrInvalidQuery
	}

	// A static query runs verbatim and stops reading instead: its SQL may be
	// DML with RETURNING, which takes no LIMIT.
	if !q.verbatim() && (!q.limit.Set || q.limit.V > limit) {
		q = q.Limit(limit)
	}

	rows, err := q.rows(ctx, options)
	if err != nil {
		return zero, err
	}

	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, fmt.Errorf("ratel: iterate SELECT: %w", err)
		}

		return zero, ErrNotFound
	}

	model, err := q.source.table.scan(rows)
	if err != nil {
		return zero, fmt.Errorf("ratel: scan SELECT: %w", err)
	}

	if limit > 1 && rows.Next() {
		return zero, ErrMultipleRows
	}

	if err := rows.Err(); err != nil {
		return zero, fmt.Errorf("ratel: iterate SELECT: %w", err)
	}

	return model, nil
}
