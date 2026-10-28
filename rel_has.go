package ratel

import "strconv"

// Has is a condition on the parent: at least one related row matches the
// condition and the relation's own Where predicates. It renders a correlated
// EXISTS, so the relation's keys must name their parent columns.
func (r Rel[M, R]) Has(condition Cond[R]) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		outer := writer.source()
		alias := "h" + strconv.Itoa(writer.depth)
		join := alias + "l"
		writer.depth++

		defer func() { writer.depth-- }()

		table := r.query.source.table

		writer.WriteString("EXISTS (SELECT 1 FROM ")
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
		writer.WriteByte(')')
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
