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
func (q Select[M]) keyset() Cond[M] {
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
			writer.qualifiedColumn(joined.table.schema, joined.table.name)
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
func (q Select[M]) ForUpdate() Select[M] { return q.locked(" FOR UPDATE") }

// ForShare locks the selected rows against concurrent updates, sharing the
// lock with other readers.
func (q Select[M]) ForShare() Select[M] { return q.locked(" FOR SHARE") }

// SkipLocked skips rows another transaction holds locked. It requires
// ForUpdate or ForShare; without one, rendering returns ErrInvalidQuery.
func (q Select[M]) SkipLocked() Select[M] { return q.waiting(" SKIP LOCKED") }

// NoWait fails instead of waiting for a row another transaction holds
// locked. It requires ForUpdate or ForShare.
func (q Select[M]) NoWait() Select[M] { return q.waiting(" NOWAIT") }

func (q Select[M]) locked(mode string) Select[M] {
	q.lock = mode

	return q
}

func (q Select[M]) waiting(policy string) Select[M] {
	q.wait = policy

	return q
}

// wrapped renders an outer statement over this SELECT as a subquery.
func (q Select[M]) wrapped(prefix, suffix string) (Statement, error) {
	inner, err := q.SQL()
	if err != nil {
		return Statement{}, err
	}

	return Statement{SQL: prefix + inner.SQL + suffix, Args: inner.Args}, nil
}

// Count returns the number of rows of the result, after Limit and Offset.
func (q Select[M]) Count(ctx context.Context, options ...CallOption) (int64, error) {
	var count int64

	err := q.scalar(ctx, "ratel.select.count", `SELECT count(*) FROM (`, `) AS "c"`, &count, options)

	return count, err
}

// Exists reports whether the result has a row.
func (q Select[M]) Exists(ctx context.Context, options ...CallOption) (bool, error) {
	var exists bool

	err := q.scalar(ctx, "ratel.select.exists", `SELECT EXISTS (`, `)`, &exists, options)

	return exists, err
}

func (q Select[M]) scalar(ctx context.Context, operation, prefix, suffix string, target any,
	options []CallOption,
) error {
	if q.db == nil {
		return ErrInvalidQuery
	}

	statement, err := q.wrapped(prefix, suffix)
	if err != nil {
		return err
	}

	ctx, span := q.db.tracer.Start(ctx, operation, q.spanOptions()...)
	defer span.End()

	err = q.db.executor.QueryRow(ctx, statement.SQL, q.db.arguments(options, statement.Args)...).Scan(target)
	if err != nil {
		err = fmt.Errorf("ratel: %s: %w", operation, err)
		recordFailure(span, err)
	}

	return err
}

// Plucked is one column of a SELECT's result.
type Plucked[V any] struct {
	db        *DB
	statement func() (Statement, error)
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
	return Plucked[V]{db: query.db, statement: func() (Statement, error) {
		writer := &sqlWriter{}
		writer.WriteString("SELECT ")
		writer.qualifiedColumn("p", name)
		writer.WriteString(" FROM (")

		if writer.invalid {
			return Statement{}, ErrInvalidQuery
		}

		statement, err := query.wrapped(writer.String(), `) AS "p"`)
		if err != nil {
			return Statement{}, err
		}

		return statement, nil
	}}
}

// List collects the column's values; an empty result is a non-nil slice.
func (p Plucked[V]) List(ctx context.Context, options ...CallOption) ([]V, error) {
	values := make([]V, 0)

	for value, err := range p.All(ctx, options...) {
		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	return values, nil
}

// All streams the column's values; breaking the loop releases the cursor.
func (p Plucked[V]) All(ctx context.Context, options ...CallOption) iter.Seq2[V, error] {
	return func(yield func(V, error) bool) {
		var zero V

		if p.db == nil {
			yield(zero, ErrInvalidQuery)

			return
		}

		statement, err := p.statement()
		if err != nil {
			yield(zero, err)

			return
		}

		ctx, span := p.db.tracer.Start(ctx, "ratel.select.pluck", spanOptions("SELECT", "")...)
		defer span.End()

		rows, err := p.db.executor.Query(ctx, statement.SQL, p.db.arguments(options, statement.Args)...)
		if err != nil {
			recordFailure(span, err)
			yield(zero, fmt.Errorf("ratel: pluck: %w", err))

			return
		}

		defer rows.Close()

		for rows.Next() {
			var value V
			if err := rows.Scan(&value); err != nil {
				recordFailure(span, err)
				yield(zero, fmt.Errorf("ratel: scan pluck: %w", err))

				return
			}

			if !yield(value, nil) {
				return
			}
		}

		if err := rows.Err(); err != nil {
			recordFailure(span, err)
			yield(zero, fmt.Errorf("ratel: iterate pluck: %w", err))
		}
	}
}
