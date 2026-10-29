package ratel

import "strconv"

// Has is a condition on the parent: at least one related row matches the
// condition and the relation's own Where predicates. It renders a correlated
// EXISTS, so the relation's keys must name their parent columns.
func (r Rel[M, R]) Has(condition Cond[R]) Cond[M] {
	return r.correlated("EXISTS (SELECT 1 FROM ", condition, ")", nil)
}

// HasNone is a condition on the parent: no related row matches the condition
// and the relation's own Where predicates.
func (r Rel[M, R]) HasNone(condition Cond[R]) Cond[M] {
	return r.Has(condition).Not()
}

// Every is a condition on the parent: every related row that passes the
// relation's own Where predicates matches the condition. A parent without
// related rows satisfies it, as a universal statement over nothing is true.
func (r Rel[M, R]) Every(condition Cond[R]) Cond[M] {
	return r.HasNone(condition.Not())
}

// HasCount compares the number of related rows that pass the relation's own
// Where predicates: app.Users.Orders.HasCount().Gt(3).
func (r Rel[M, R]) HasCount() RelCountCond[M, R] {
	return RelCountCond[M, R]{relation: r}
}

// RelCountCond is a relation's row count, compared into a condition on the
// parent.
type RelCountCond[M, R any] struct {
	relation Rel[M, R]
}

// Eq holds when the parent has exactly count related rows.
func (c RelCountCond[M, R]) Eq(count int64) Cond[M] { return c.compare(" = ", count) }

// Ne holds when the parent has another number of related rows.
func (c RelCountCond[M, R]) Ne(count int64) Cond[M] { return c.compare(" <> ", count) }

// Lt holds when the parent has fewer related rows.
func (c RelCountCond[M, R]) Lt(count int64) Cond[M] { return c.compare(" < ", count) }

// Lte holds when the parent has at most count related rows.
func (c RelCountCond[M, R]) Lte(count int64) Cond[M] { return c.compare(" <= ", count) }

// Gt holds when the parent has more related rows.
func (c RelCountCond[M, R]) Gt(count int64) Cond[M] { return c.compare(" > ", count) }

// Gte holds when the parent has at least count related rows.
func (c RelCountCond[M, R]) Gte(count int64) Cond[M] { return c.compare(" >= ", count) }

func (c RelCountCond[M, R]) compare(operator string, count int64) Cond[M] {
	return c.relation.correlated("(SELECT count(*) FROM ", Cond[R]{}, ")", func(writer *sqlWriter) {
		writer.WriteString(operator)
		writer.param(count)
	})
}

// correlated renders a subquery over the relation's target correlated with
// the parent through the keys: head, the target and join table, the key
// predicates, the relation's Where and condition, close, then tail.
func (r Rel[M, R]) correlated(head string, condition Cond[R], closing string, tail func(*sqlWriter)) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		outer := writer.source()
		alias := "h" + strconv.Itoa(writer.depth)
		join := alias + "l"
		writer.depth++

		defer func() { writer.depth-- }()

		table := r.query.source.table

		writer.WriteString(head)
		writer.qualifiedColumn(table.schema, table.name)
		writer.WriteString(" AS ")
		writer.identifier(alias)

		match := alias
		if len(r.through) != 0 {
			match = join
			r.appendJoin(writer, alias, join)
		}

		writer.WriteString(" WHERE ")

		for _, key := range r.keys {
			if key.Parent == "" {
				writer.invalid = true

				return
			}

			writer.qualifiedColumn(match, key.Column)
			writer.WriteString(" = ")
			writer.qualifiedColumn(outer, key.Parent)
			writer.WriteString(" AND ")
		}

		previous := writer.alias
		writer.alias = alias

		if r.query.hasWhere {
			r.query.where.And(condition).appendSQL(writer)
		} else {
			condition.appendSQL(writer)
		}

		writer.alias = previous
		writer.WriteString(closing)

		if tail != nil {
			tail(writer)
		}
	}}
}

// appendJoin joins a many-to-many relation's join table to the target.
func (r Rel[M, R]) appendJoin(writer *sqlWriter, target, join string) {
	writer.WriteString(" JOIN ")
	writer.qualifiedColumn(r.through[0], r.through[1])
	writer.WriteString(" AS ")
	writer.identifier(join)
	writer.WriteString(" ON ")

	for index, column := range r.targetColumns {
		if index > 0 {
			writer.WriteString(" AND ")
		}

		writer.qualifiedColumn(join, column)
		writer.WriteString(" = ")
		writer.qualifiedColumn(target, r.query.source.table.keys[index])
	}
}
