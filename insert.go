package ratel

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Insert is an immutable INSERT or ON CONFLICT builder.
type Insert[M, I any] struct {
	db        *DB
	table     WriteTable[M, I]
	rows      []I
	upsert    bool
	conflict  []string
	updates   []string
	doNothing bool
}

// Insert inserts values built by the generated constructor.
func (db *DB) Insert[M, I any](table Writable[M, I], rows ...I) Insert[M, I] {
	return Insert[M, I]{db: db, table: table.ratelWrite(), rows: slices.Clone(rows)}
}

// Upsert starts an INSERT requiring an explicit conflict target and action.
func (db *DB) Upsert[M, I any](table Writable[M, I], rows ...I) Insert[M, I] {
	query := db.Insert(table, rows...)
	query.upsert = true

	return query
}

// OnConflict selects the ordered columns of a unique conflict target.
func (q Insert[M, I]) OnConflict(columns ...WriteColumn[M]) Insert[M, I] {
	q.upsert = true
	q.conflict = writeColumnNames(columns)

	return q
}

// Update replaces the selected columns with their EXCLUDED values.
func (q Insert[M, I]) Update(columns ...WriteColumn[M]) Insert[M, I] {
	q.upsert = true
	q.updates = writeColumnNames(columns)
	q.doNothing = false

	return q
}

// DoNothing skips conflicting rows; skipped rows are not returned.
func (q Insert[M, I]) DoNothing() Insert[M, I] {
	q.upsert = true
	q.updates = nil
	q.doNothing = true

	return q
}

func writeColumnNames[M any](columns []WriteColumn[M]) []string {
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = column.ratelWriteColumn()
	}

	return names
}

// SQL renders a single statement. Use Statements for heterogeneous defaults or
// batches larger than WithBatchRows, which require several statements.
func (q Insert[M, I]) SQL() (Statement, error) {
	statements, err := q.Statements()
	if err != nil {
		return Statement{}, err
	}

	if len(statements) != 1 {
		return Statement{}, ErrInvalidQuery
	}

	return statements[0], nil
}

// Statements renders every statement before any can be executed. Consecutive
// rows with the same set columns share a statement; input order is preserved.
func (q Insert[M, I]) Statements() ([]Statement, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}

	statements := make([]Statement, 0, 1)

	for start := 0; start < len(q.rows); {
		fields := q.setFields(q.rows[start])
		limit := q.rowLimit(fields)
		end := start + 1

		for end < len(q.rows) && end-start < limit && len(fields) > 0 && q.sets(q.rows[end], fields) {
			end++
		}

		statement, err := q.render(fields, q.rows[start:end])
		if err != nil {
			return nil, err
		}

		statements = append(statements, statement)
		start = end
	}

	return statements, nil
}

func (q Insert[M, I]) validate() error {
	if len(q.rows) == 0 || q.table.validInsert == nil {
		return ErrInvalidInsert
	}

	for _, row := range q.rows {
		if !q.table.validInsert(row) {
			return ErrInvalidInsert
		}
	}

	if q.db == nil || q.db.batchRows <= 0 || !q.table.valid() {
		return ErrInvalidQuery
	}

	if q.upsert && (len(q.conflict) == 0 || (!q.doNothing && len(q.updates) == 0)) {
		return ErrInvalidQuery
	}

	if !validWriteNames(q.table.columns, q.conflict) || !validWriteNames(q.table.columns, q.updates) {
		return ErrInvalidQuery
	}

	return q.validateFields()
}

func (q Insert[M, I]) validateFields() error {
	names := make([]string, len(q.table.fields))

	for index, field := range q.table.fields {
		if field.Value == nil || len(field.Type) == 0 || (!field.Array && field.Values == nil) {
			return ErrInvalidQuery
		}

		for _, part := range field.Type {
			if !validIdentifier(part) {
				return ErrInvalidQuery
			}
		}

		names[index] = field.Name
	}

	if !validWriteNames(q.table.columns, names) {
		return ErrInvalidQuery
	}

	return nil
}

func validWriteNames(columns, names []string) bool {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !slices.Contains(columns, name) || seen[name] {
			return false
		}

		seen[name] = true
	}

	return true
}

func (q Insert[M, I]) rowLimit(fields []int) int {
	// The extended PostgreSQL protocol limits a statement to 65535 parameters.
	const parameterLimit = 65535

	if len(fields) > 0 && !q.scalarFields(fields) {
		return min(q.db.batchRows, parameterLimit/len(fields))
	}

	return q.db.batchRows
}

// setFields lists the fields a row sets, by index.
func (q Insert[M, I]) setFields(row I) []int {
	fields := make([]int, 0, len(q.table.fields))

	for index, field := range q.table.fields {
		if field.set(row) {
			fields = append(fields, index)
		}
	}

	return fields
}

// sets reports whether a row sets exactly the given fields, without
// allocating.
func (q Insert[M, I]) sets(row I, fields []int) bool {
	next := 0

	for index, field := range q.table.fields {
		listed := next < len(fields) && fields[next] == index
		if field.set(row) != listed {
			return false
		}

		if listed {
			next++
		}
	}

	return true
}

func (q Insert[M, I]) render(fields []int, inputs []I) (Statement, error) {
	writer := newSQLWriter()
	writer.WriteString("INSERT INTO ")

	if err := q.table.render(writer); err != nil {
		return Statement{}, err
	}

	writer.WriteString(` AS "q"`)

	if len(fields) == 0 {
		writer.WriteString(" DEFAULT VALUES")
	} else {
		q.appendColumns(writer, fields)

		if len(inputs) > 1 && q.scalarFields(fields) {
			q.appendUnnest(writer, fields, inputs)
		} else {
			q.appendValues(writer, fields, inputs)
		}
	}

	if q.upsert {
		q.appendConflict(writer)
	}

	return writer.statement(), nil
}

func (q Insert[M, I]) scalarFields(fields []int) bool {
	for _, index := range fields {
		if q.table.fields[index].Array {
			return false
		}
	}

	return true
}

func (q Insert[M, I]) appendColumns(writer *sqlWriter, fields []int) {
	writer.WriteString(" (")

	for index, field := range fields {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.WriteString(pgx.Identifier{q.table.fields[field].Name}.Sanitize())
	}

	writer.WriteByte(')')
}

func (q Insert[M, I]) appendUnnest(writer *sqlWriter, fields []int, rows []I) {
	writer.WriteString(" SELECT * FROM unnest(")

	for index, field := range fields {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.param(q.table.fields[field].Values(rows))
		writer.WriteString("::")
		writer.WriteString(q.table.fields[field].Type.Sanitize())
		writer.WriteString("[]")
	}

	writer.WriteByte(')')
}

func (q Insert[M, I]) appendValues(writer *sqlWriter, fields []int, rows []I) {
	writer.WriteString(" VALUES ")

	for rowIndex, row := range rows {
		if rowIndex > 0 {
			writer.WriteString(", ")
		}

		writer.WriteByte('(')

		for index, field := range fields {
			if index > 0 {
				writer.WriteString(", ")
			}

			value, _ := q.table.fields[field].Value(row)
			writer.param(value)
		}

		writer.WriteByte(')')
	}
}

func (q Insert[M, I]) appendConflict(writer *sqlWriter) {
	writer.WriteString(" ON CONFLICT (")
	appendWriteNames(writer, q.conflict)
	writer.WriteString(") DO ")

	if q.doNothing {
		writer.WriteString("NOTHING")

		return
	}

	writer.WriteString("UPDATE SET ")

	for index, column := range q.updates {
		if index > 0 {
			writer.WriteString(", ")
		}

		name := pgx.Identifier{column}.Sanitize()
		writer.WriteString(name)
		writer.WriteString(" = EXCLUDED.")
		writer.WriteString(name)
	}
}

func appendWriteNames(writer *sqlWriter, names []string) {
	for index, name := range names {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.WriteString(pgx.Identifier{name}.Sanitize())
	}
}

// Exec returns the total affected rows. On failure it returns zero, even if an
// earlier chunk committed. Use a caller-owned transaction for atomic chunks.
func (q Insert[M, I]) Exec(ctx context.Context, options ...CallOption) (int64, error) {
	statements, err := q.Statements()
	if err != nil {
		return 0, err
	}

	result, err := executeWrite(ctx, q.db, q.table.Table, statements, false, options)

	return result.count, err
}

// Returning scans PostgreSQL RETURNING, including defaults and BEFORE triggers.
// No partial slice is returned on failure. Row order is PostgreSQL's order.
func (q Insert[M, I]) Returning(ctx context.Context, options ...CallOption) ([]M, error) {
	statements, err := q.Statements()
	if err != nil {
		return nil, err
	}

	result, err := executeWrite(ctx, q.db, q.table.Table, statements, true, options)

	return result.models, err
}
