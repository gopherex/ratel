package ratel

import (
	"context"
	"iter"
	"slices"
)

// Chunks reads keyset pages in ascending primary-key order, closing each cursor
// before yielding. It respects the query's total Limit and initial Offset.
// Missing primary-key metadata, nonpositive sizes, or incompatible ordering
// yield ErrInvalidQuery before executing SQL. Page reads do not share a snapshot
// unless the caller's executor supplies a transaction.
func (r SelectRunner[M]) Chunks(ctx context.Context, size int, options ...CallOption) iter.Seq2[[]M, error] {
	return func(yield func([]M, error) bool) {
		if r.db == nil || !r.query.chunkable(size) {
			yield(nil, ErrInvalidQuery)

			return
		}

		if _, _, err := r.query.Build(); err != nil {
			yield(nil, err)

			return
		}

		r.pages(ctx, size, options, yield)
	}
}

func (q *Select[M]) chunkable(size int) bool {
	table := q.source.table
	if size <= 0 || !table.valid() || len(table.keys) == 0 || table.keyValues == nil {
		return false
	}

	if q.limit.V < 0 || q.offset.V < 0 {
		return false
	}

	for _, key := range table.keys {
		if !slices.Contains(table.columns, key) {
			return false
		}
	}

	if len(q.orders) == 0 {
		return true
	}

	if len(q.orders) != len(table.keys) {
		return false
	}

	for index, order := range q.orders {
		if order.name != table.keys[index] || order.descending {
			return false
		}
	}

	return true
}

func (r *SelectRunner[M]) pages(ctx context.Context, size int, options []CallOption, yield func([]M, error) bool) {
	query := r.query
	remaining := query.limit

	query.orders = make([]Order[M], len(query.source.table.keys))
	for index, key := range query.source.table.keys {
		query.orders[index] = Order[M]{name: key}
	}

	var cursor []any

	for !remaining.Set || remaining.V > 0 {
		count := size
		if remaining.Set && remaining.V < count {
			count = remaining.V
		}

		pageQuery := query.Limit(count)
		if cursor != nil {
			pageQuery = pageQuery.Where(query.afterKey(cursor))
		}

		page, err := SelectRunner[M]{db: r.db, query: pageQuery}.List(ctx, options...)
		if err != nil {
			yield(nil, err)

			return
		}

		if len(page) == 0 {
			return
		}

		cursor = slices.Clone(query.source.table.keyValues(page[len(page)-1]))
		if len(cursor) != len(query.source.table.keys) {
			yield(nil, ErrInvalidQuery)

			return
		}

		if !yield(page, nil) || len(page) < count {
			return
		}

		if remaining.Set {
			remaining.V -= len(page)
		}

		query.offset = Opt[int]{}
	}
}

func (q Select[M]) afterKey(values []any) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.WriteByte('(')
		for index, key := range q.source.table.keys {
			if index > 0 {
				writer.WriteString(", ")
			}
			writer.column(key)
		}
		writer.WriteString(") > (")
		for index, value := range values {
			if index > 0 {
				writer.WriteString(", ")
			}
			writer.param(value)
		}
		writer.WriteByte(')')
	}}
}
