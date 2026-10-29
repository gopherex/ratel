package ratel

import (
	"context"
	"fmt"
	"iter"
)

// After continues keyset pagination after the row whose ordering values are
// given, one per OrderBy term in order. Mixed directions are supported; the
// ordering columns must be non-null, as NULL compares as unknown.
func (q Select[M]) After(values ...any) Select[M] {
	q.after = append([]any{}, values...)

	return q
}

// keyset expands (c1, c2) after (v1, v2) per direction:
// c1 > v1 OR (c1 = v1 AND c2 > v2), with < for descending terms.
func (q *Select[M]) keyset() Cond[M] {
	orders, values := q.orders, q.after

	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.WriteByte('(')

		for index := range orders {
			if index > 0 {
				writer.WriteString(" OR ")
			}

			writer.WriteByte('(')

			for previous := range index {
				writer.column(orders[previous].name)
				writer.WriteString(" = ")
				writer.param(values[previous])
				writer.WriteString(" AND ")
			}

			writer.column(orders[index].name)

			if orders[index].descending {
				writer.WriteString(" < ")
			} else {
				writer.WriteString(" > ")
			}

			writer.param(values[index])
			writer.WriteByte(')')
		}

		writer.WriteByte(')')
	}}
}

// Join inner-joins another source on a typed join predicate; the result stays
// the query's model. A row repeats once per match: for existence, use a
// relation's Has.
func (q Select[M]) Join[N any](other Source[N], predicate Cond2[M, N]) Select[M] {
	joined := other.ratelSource()

	q.joins = append(append([]func(*sqlWriter, string) error{}, q.joins...), func(writer *sqlWriter, alias string) error {
		writer.WriteString(" JOIN ")

		if joined.render == nil {
			if err := joined.table.render(writer); err != nil {
				return err
			}
		} else if err := joined.render(writer); err != nil {
			return err
		}

		writer.WriteString(" AS ")
		writer.identifier(alias)
		writer.WriteString(" ON ")
		predicate.appendSQL(writer, "q", alias)

		return nil
	})

	return q
}

// ForUpdate locks the selected rows against concurrent updates and deletes
// until the transaction ends.
func (q Select[M]) ForUpdate() Select[M] { return q.locked(lockUpdate) }

// ForShare locks the selected rows against concurrent updates, sharing the
// lock with other readers.
func (q Select[M]) ForShare() Select[M] { return q.locked(lockShare) }

// SkipLocked skips rows another transaction holds locked. It requires
// ForUpdate or ForShare; without one, rendering returns ErrInvalidQuery.
func (q Select[M]) SkipLocked() Select[M] { return q.waiting(waitSkipLocked) }

// NoWait fails instead of waiting for a row another transaction holds
// locked. It requires ForUpdate or ForShare.
func (q Select[M]) NoWait() Select[M] { return q.waiting(waitNoWait) }

func (q Select[M]) locked(mode lockMode) Select[M] {
	q.lock = mode

	return q
}

func (q Select[M]) waiting(policy waitMode) Select[M] {
	q.wait = policy

	return q
}

// wrapped renders an outer statement over this SELECT as a subquery.
func (q *Select[M]) wrapped(prefix, suffix string) (stmt, error) {
	sql, args, err := q.Build()
	if err != nil {
		return stmt{}, err
	}

	return stmt{SQL: prefix + sql + suffix, Args: args}, nil
}

// Count returns the number of rows of the result, after Limit and Offset.
func (r SelectRunner[M]) Count(ctx context.Context, options ...CallOption) (int64, error) {
	var count int64

	err := r.scalar(ctx, "ratel.select.count", `SELECT count(*) FROM (`, `) AS "c"`, &count, options)

	return count, err
}

// Exists reports whether the result has a row.
func (r SelectRunner[M]) Exists(ctx context.Context, options ...CallOption) (bool, error) {
	var exists bool

	err := r.scalar(ctx, "ratel.select.exists", `SELECT EXISTS (`, `)`, &exists, options)

	return exists, err
}

func (r *SelectRunner[M]) scalar(ctx context.Context, operation, prefix, suffix string, target any,
	options []CallOption,
) error {
	if r.db == nil {
		return ErrInvalidQuery
	}

	rendered, err := r.query.wrapped(prefix, suffix)
	if err != nil {
		return err
	}

	err = r.db.executor.QueryRow(ctx, rendered.SQL, r.db.arguments(options, rendered.Args)...).Scan(target)
	if err != nil {
		return fmt.Errorf("ratel: %s: %w", operation, err)
	}

	return nil
}

// Plucked is one column of a SELECT's result, a value that renders through
// Build and, run by db.Run, yields the column's values.
type Plucked[V any] struct {
	render func() (stmt, error)
}

// Pluck selects one non-null column of the result.
func (q Select[M]) Pluck[V any](column Col[M, V]) Plucked[V] {
	return pluck[V](q, column.name)
}

// PluckNull selects one nullable column of the result.
func (q Select[M]) PluckNull[V any](column NullCol[M, V]) Plucked[Null[V]] {
	return pluck[Null[V]](q, column.name)
}

func pluck[V, M any](query Select[M], name string) Plucked[V] {
	return Plucked[V]{render: func() (stmt, error) {
		writer := newSQLWriter()
		writer.WriteString("SELECT ")
		writer.qualifiedColumn("p", name)
		writer.WriteString(" FROM (")

		if writer.invalid {
			return stmt{}, ErrInvalidQuery
		}

		return query.wrapped(writer.String(), `) AS "p"`)
	}}
}

// Build renders the column query and its arguments without executing it.
func (p Plucked[V]) Build() (string, []any, error) {
	if p.render == nil {
		return "", nil, ErrInvalidQuery
	}

	rendered, err := p.render()

	return rendered.SQL, rendered.Args, err
}

// PluckRunner is a Plucked column bound to a database by db.Run.
type PluckRunner[V any] struct {
	db     *DB
	column Plucked[V]
}

func (p Plucked[V]) bind(db *DB) PluckRunner[V] { return PluckRunner[V]{db: db, column: p} }

// List collects the column's values; an empty result is a non-nil slice.
func (r PluckRunner[V]) List(ctx context.Context, options ...CallOption) ([]V, error) {
	values := make([]V, 0)

	for value, err := range r.All(ctx, options...) {
		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	return values, nil
}

// All streams the column's values; breaking the loop releases the cursor.
func (r PluckRunner[V]) All(ctx context.Context, options ...CallOption) iter.Seq2[V, error] {
	return func(yield func(V, error) bool) {
		var zero V

		if r.db == nil {
			yield(zero, ErrInvalidQuery)

			return
		}

		sql, args, err := r.column.Build()
		if err != nil {
			yield(zero, err)

			return
		}

		rows, err := r.db.executor.Query(ctx, sql, r.db.arguments(options, args)...)
		if err != nil {
			yield(zero, fmt.Errorf("ratel: pluck: %w", err))

			return
		}

		defer rows.Close()

		for rows.Next() {
			var value V
			if err := rows.Scan(&value); err != nil {
				yield(zero, fmt.Errorf("ratel: scan pluck: %w", err))

				return
			}

			if !yield(value, nil) {
				return
			}
		}

		if err := rows.Err(); err != nil {
			yield(zero, fmt.Errorf("ratel: iterate pluck: %w", err))
		}
	}
}
