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
func (q Select[M]) Chunks(ctx context.Context, size int, options ...CallOption) iter.Seq2[[]M, error] {
	return func(yield func([]M, error) bool) {
		if !q.chunkable(size) {
			yield(nil, ErrInvalidQuery)

			return
		}

		if _, err := q.SQL(); err != nil {
			yield(nil, err)

			return
		}

		q.pages(ctx, size, options, yield)
	}
}

func (q Select[M]) chunkable(size int) bool {
	table := q.source.table
	if q.db == nil || size <= 0 || !table.valid() || len(table.keys) == 0 || table.keyValues == nil {
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

func (q Select[M]) pages(ctx context.Context, size int, options []CallOption, yield func([]M, error) bool) {
	remaining := q.limit

	q.orders = make([]Order[M], len(q.source.table.keys))
	for index, key := range q.source.table.keys {
		q.orders[index] = Order[M]{name: key}
	}

	var cursor []any

	for !remaining.Set || remaining.V > 0 {
		count := size
		if remaining.Set && remaining.V < count {
			count = remaining.V
		}

		pageQuery := q.Limit(count)
		if cursor != nil {
			pageQuery = pageQuery.Where(q.afterKey(cursor))
		}

		page, err := pageQuery.List(ctx, options...)
		if err != nil {
			yield(nil, err)

			return
		}

		if len(page) == 0 {
			return
		}

		cursor = slices.Clone(q.source.table.keyValues(page[len(page)-1]))
		if len(cursor) != len(q.source.table.keys) {
			yield(nil, ErrInvalidQuery)

			return
		}

		if !yield(page, nil) || len(page) < count {
			return
		}

		if remaining.Set {
			remaining.V -= len(page)
		}

		q.offset = Opt[int]{}
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
