package ratel

import (
	"slices"
	"strconv"
)

func (r Rel[M, R]) valid(count bool) bool {
	if !r.validTarget() || !r.validKeys() || r.attach == nil || r.query.limit.V < 0 || r.query.offset.V < 0 {
		return false
	}

	if r.cardinality != RelOne && r.cardinality != RelMany {
		return false
	}

	if count && r.count == nil {
		return false
	}

	if len(r.through) != 0 && !r.validThrough() {
		return false
	}

	return r.validStrategy(count)
}

func (r Rel[M, R]) validTarget() bool {
	table := r.query.source.table
	if !table.valid() || !validIdentifier(table.schema) || !validIdentifier(table.name) ||
		len(table.keys) == 0 || table.keyValues == nil {
		return false
	}

	for _, key := range table.keys {
		if !slices.Contains(table.columns, key) {
			return false
		}
	}

	return true
}

func (r Rel[M, R]) validKeys() bool {
	if len(r.keys) == 0 {
		return false
	}

	for _, key := range r.keys {
		if !validIdentifier(key.Column) || len(key.Type) != 2 || key.Values == nil {
			return false
		}

		for _, part := range key.Type {
			if !validIdentifier(part) {
				return false
			}
		}

		if len(r.through) == 0 && !slices.Contains(r.query.source.table.columns, key.Column) {
			return false
		}
	}

	return true
}

func (r Rel[M, R]) validStrategy(count bool) bool {
	switch r.strategy.kind {
	case strategyDefault:
		return true
	case strategyWindow:
		return !count
	case strategyCorrelated:
		return count
	case strategyJoined:
		return !count && r.cardinality == RelOne && !r.query.limit.Set && !r.query.offset.Set
	default:
		return false
	}
}

func (r Rel[M, R]) validThrough() bool {
	if len(r.through) != 2 || len(r.targetColumns) != len(r.query.source.table.keys) {
		return false
	}

	for _, name := range append(slices.Clone(r.through), r.targetColumns...) {
		if !validIdentifier(name) {
			return false
		}
	}

	return true
}

func (r Rel[M, R]) compile() Rel[M, R] {
	rows, _ := r.renderStatement(false)
	count, _ := r.renderStatement(true)
	r.statements = &relationStatements{rows: rows, count: count}

	return r
}

func (r Rel[M, R]) statement(models []M, count bool) (stmt, error) {
	if r.statements == nil {
		return stmt{}, ErrInvalidQuery
	}

	rendered := r.statements.rows
	if count {
		rendered = r.statements.count
	}

	if rendered.SQL == "" {
		return stmt{}, ErrInvalidQuery
	}

	rendered.Args = slices.Clone(rendered.Args)
	for index, key := range r.keys {
		rendered.Args[index] = key.Values(models)
	}

	return rendered, nil
}

func (r Rel[M, R]) renderStatement(count bool) (stmt, error) {
	if !r.valid(count) {
		return stmt{}, ErrInvalidQuery
	}

	writer := newSQLWriter()

	switch {
	case count:
		r.appendCount(writer)
	case r.strategy.kind == strategyWindow:
		r.appendWindow(writer)
	case r.query.limit.Set || r.query.offset.Set:
		r.appendLateral(writer)
	default:
		r.appendJoined(writer)
	}

	if writer.invalid {
		return stmt{}, ErrInvalidQuery
	}

	return writer.result(), nil
}

func (r Rel[M, R]) internalColumn(name string) string {
	for slices.Contains(r.query.source.table.columns, name) {
		name += "_"
	}

	return name
}

func (r Rel[M, R]) appendInput(writer *sqlWriter) {
	writer.WriteString("unnest(")

	for index, key := range r.keys {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.param(nil)
		writer.WriteString("::")
		writer.qualifiedColumn(key.Type[0], key.Type[1])
		writer.WriteString("[]")
	}

	writer.WriteString(`) WITH ORDINALITY AS "p" (`)

	for index := range r.keys {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.identifier(relationKeyName(index))
	}

	writer.WriteString(`, "__ratel_parent")`)
}

func relationKeyName(index int) string { return "k" + strconv.Itoa(index) }

func (r Rel[M, R]) appendColumns(writer *sqlWriter, alias string) {
	for index, column := range r.query.source.table.columns {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.qualifiedColumn(alias, column)
	}
}

func (r Rel[M, R]) foreignAlias() string {
	if len(r.through) != 0 {
		return "l"
	}

	return "q"
}

func (r Rel[M, R]) appendTarget(writer *sqlWriter) {
	table := r.query.source.table
	writer.qualifiedColumn(table.schema, table.name)
	writer.WriteString(` AS "q"`)

	if len(r.through) == 0 {
		return
	}

	writer.WriteString(" JOIN ")
	writer.qualifiedColumn(r.through[0], r.through[1])
	writer.WriteString(` AS "l" ON `)

	for index, column := range r.targetColumns {
		if index > 0 {
			writer.WriteString(" AND ")
		}

		writer.qualifiedColumn("l", column)
		writer.WriteString(" = ")
		writer.qualifiedColumn("q", table.keys[index])
	}
}

func (r Rel[M, R]) appendMatch(writer *sqlWriter) {
	for index, key := range r.keys {
		if index > 0 {
			writer.WriteString(" AND ")
		}

		writer.qualifiedColumn(r.foreignAlias(), key.Column)
		writer.WriteString(" = ")
		writer.qualifiedColumn("p", relationKeyName(index))
	}
}

func (r Rel[M, R]) appendFilter(writer *sqlWriter) {
	if r.query.hasWhere {
		writer.WriteString(" AND (")
		r.query.where.appendSQL(writer)
		writer.WriteByte(')')
	}
}

func (r Rel[M, R]) appendBody(writer *sqlWriter) {
	r.appendInput(writer)
	writer.WriteString(" JOIN ")

	if len(r.through) != 0 {
		writer.WriteByte('(')
	}

	r.appendTarget(writer)

	if len(r.through) != 0 {
		writer.WriteByte(')')
	}

	writer.WriteString(" ON ")
	r.appendMatch(writer)
	writer.WriteString(" WHERE TRUE")

	if len(r.keys) == 1 {
		writer.WriteString(" AND ")
		writer.qualifiedColumn(r.foreignAlias(), r.keys[0].Column)
		writer.WriteString(" = ANY($1)")
	}

	r.appendFilter(writer)
}

func (r Rel[M, R]) appendJoined(writer *sqlWriter) {
	writer.WriteString(`SELECT "p"."__ratel_parent", `)
	r.appendColumns(writer, "q")
	writer.WriteString(" FROM ")
	r.appendBody(writer)
	r.appendChildOrder(writer, "q", false)
}

func (r Rel[M, R]) appendChildOrder(writer *sqlWriter, alias string, comma bool) {
	for index, order := range r.query.orders {
		if comma || index > 0 {
			writer.WriteString(", ")
		} else {
			writer.WriteString(" ORDER BY ")
		}

		writer.qualifiedColumn(alias, order.name)

		if order.descending {
			writer.WriteString(" DESC")
		} else {
			writer.WriteString(" ASC")
		}
	}
}

func (r Rel[M, R]) appendLateral(writer *sqlWriter) {
	writer.WriteString(`SELECT "p"."__ratel_parent", `)
	r.appendColumns(writer, "c")
	writer.WriteString(" FROM ")
	r.appendInput(writer)
	writer.WriteString(" CROSS JOIN LATERAL (SELECT ")
	r.appendColumns(writer, "q")
	writer.WriteString(" FROM ")
	r.appendTarget(writer)
	writer.WriteString(" WHERE ")
	r.appendMatch(writer)
	r.appendFilter(writer)
	r.appendChildOrder(writer, "q", false)
	r.query.appendPagination(writer)
	writer.WriteString(`) AS "c"`)
	r.appendChildOrder(writer, "c", false)
}

func (r Rel[M, R]) appendWindow(writer *sqlWriter) {
	parent := r.internalColumn("__ratel_parent")
	rank := r.internalColumn("__ratel_rank")

	writer.WriteString("SELECT ")
	writer.qualifiedColumn("w", parent)
	writer.WriteString(", ")
	r.appendColumns(writer, "w")
	writer.WriteString(` FROM (SELECT "p"."__ratel_parent" AS `)
	writer.identifier(parent)
	writer.WriteString(", ")
	r.appendColumns(writer, "q")
	writer.WriteString(`, row_number() OVER (PARTITION BY "p"."__ratel_parent"`)
	r.appendChildOrder(writer, "q", false)
	writer.WriteString(") AS ")
	writer.identifier(rank)
	writer.WriteString(" FROM ")
	r.appendBody(writer)
	writer.WriteString(`) AS "w" WHERE `)
	writer.qualifiedColumn("w", rank)
	writer.WriteString(" > ")
	writer.param(r.query.offset.V)

	if r.query.limit.Set {
		writer.WriteString(" AND ")
		writer.qualifiedColumn("w", rank)
		writer.WriteString(" - ")
		writer.param(r.query.offset.V)
		writer.WriteString(" <= ")
		writer.param(r.query.limit.V)
	}

	// The rank is the child order within each parent; grouping by parent is
	// left to the loader.
	if len(r.query.orders) > 0 {
		writer.WriteString(" ORDER BY ")
		writer.qualifiedColumn("w", rank)
	}
}

func (r Rel[M, R]) appendCount(writer *sqlWriter) {
	writer.WriteString(`SELECT "p"."__ratel_parent", `)

	if r.strategy.kind == strategyCorrelated {
		r.appendCorrelatedCount(writer)

		return
	}

	writer.WriteString("count(*) FROM ")
	r.appendBody(writer)
	writer.WriteString(` GROUP BY "p"."__ratel_parent"`)

	for _, key := range r.keys {
		writer.WriteString(", ")
		writer.qualifiedColumn(r.foreignAlias(), key.Column)
	}
}

func (r Rel[M, R]) appendCorrelatedCount(writer *sqlWriter) {
	// Bind parent arrays first so child predicates always follow the key parameters.
	input := newSQLWriter()
	r.appendInput(input)
	writer.args = input.args
	writer.WriteString("(SELECT count(*) FROM ")
	r.appendTarget(writer)
	writer.WriteString(" WHERE ")
	r.appendMatch(writer)
	r.appendFilter(writer)
	writer.WriteString(") FROM ")
	writer.WriteString(input.String())
	writer.invalid = writer.invalid || input.invalid
}
