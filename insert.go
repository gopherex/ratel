package ratel

import (
	"slices"

	"github.com/jackc/pgx/v5"
)

// defaultChunk is the default number of rows per insert statement.
const defaultChunk = 1000

// Insert is an immutable INSERT or ON CONFLICT statement. It is a value:
// Build renders the one statement it needs, db.Run(insert).Exec sends all of
// them, and Returning turns it into a row query.
type Insert[M, I any] struct {
	table     WriteTable[M, I]
	rows      []I
	chunk     Opt[int]
	upsert    bool
	conflict  []string
	updates   []string
	doNothing bool
}

// Insert inserts values built by the generated constructor.
func (t WriteTable[M, I]) Insert(rows ...I) Insert[M, I] {
	return Insert[M, I]{table: t, rows: slices.Clone(rows)}
}

// Upsert starts an INSERT requiring an explicit conflict target and action.
func (t WriteTable[M, I]) Upsert(rows ...I) Insert[M, I] {
	query := t.Insert(rows...)
	query.upsert = true

	return query
}

// Chunk caps the rows per statement. Default: 1000. Nonpositive values are
// ErrInvalidQuery. The size belongs to the value, so Build and Run agree on it;
// chunk atomicity belongs to the executor.
func (q Insert[M, I]) Chunk(rows int) Insert[M, I] {
	q.chunk = OptFrom(rows)

	return q
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

// Build renders the one statement the insert needs. An insert that needs
// several (more rows than its chunk size, or rows with different sets of
// optional columns) is ErrInvalidQuery; db.Run sends all of them.
func (q Insert[M, I]) Build() (string, []any, error) {
	rendered, err := q.statements()
	if err != nil {
		return "", nil, err
	}

	if len(rendered) != 1 {
		return "", nil, ErrInvalidQuery
	}

	return rendered[0].SQL, rendered[0].Args, nil
}

// statements renders every statement before any can be executed. Consecutive
// rows with the same set columns share a statement; input order is preserved.
func (q Insert[M, I]) statements() ([]stmt, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}

	rendered := make([]stmt, 0, 1)

	for start := 0; start < len(q.rows); {
		fields := q.setFields(q.rows[start])
		limit := q.rowLimit(fields)
		end := start + 1

		for end < len(q.rows) && end-start < limit && len(fields) > 0 && q.sets(q.rows[end], fields) {
			end++
		}

		next, err := q.render(fields, q.rows[start:end])
		if err != nil {
			return nil, err
		}

		rendered = append(rendered, next)
		start = end
	}

	return rendered, nil
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

	if (q.chunk.Set && q.chunk.V <= 0) || !q.table.valid() {
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

	size := defaultChunk
	if q.chunk.Set {
		size = q.chunk.V
	}

	if len(fields) > 0 && !q.scalarFields(fields) {
		return min(size, parameterLimit/len(fields))
	}

	return size
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

func (q Insert[M, I]) render(fields []int, inputs []I) (stmt, error) {
	writer := newSQLWriter()
	writer.WriteString("INSERT INTO ")

	if err := q.table.render(writer); err != nil {
		return stmt{}, err
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

	return writer.result(), nil
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

// Returning adds RETURNING of every model column, including defaults and
// BEFORE trigger values, and turns the insert into a row query. Conflicts
// skipped by DO NOTHING return no row; row order is PostgreSQL's order.
func (q Insert[M, I]) Returning() InsertReturning[M, I] { return InsertReturning[M, I]{insert: q} }

// InsertReturning is an INSERT with RETURNING, a row query.
type InsertReturning[M, I any] struct{ insert Insert[M, I] }

// Build renders the one statement the insert needs, with its RETURNING clause;
// several statements are ErrInvalidQuery as for Insert.
func (q InsertReturning[M, I]) Build() (string, []any, error) {
	rendered, err := q.insert.statements()
	if err != nil {
		return "", nil, err
	}

	if len(rendered) != 1 {
		return "", nil, ErrInvalidQuery
	}

	rendered[0] = returning(rendered[0], q.insert.table.Table)

	return rendered[0].SQL, rendered[0].Args, nil
}

func (q Insert[M, I]) execPlan() execPlan {
	rendered, err := q.statements()

	return execPlan{plan: multiPlan(rendered, err), target: q.table.target()}
}

func (q Insert[M, I]) bind(db *DB) ExecRunner { return ExecRunner{db: db, plan: q.execPlan()} }

func (q InsertReturning[M, I]) rowsPlan() rowsPlan[M] {
	rendered, err := q.insert.statements()
	for index := range rendered {
		rendered[index] = returning(rendered[index], q.insert.table.Table)
	}

	return rowsPlan[M]{plan: multiPlan(rendered, err), table: q.insert.table.Table, write: true}
}

func (q InsertReturning[M, I]) bind(db *DB) RowRunner[M] {
	return RowRunner[M]{db: db, plan: q.rowsPlan()}
}
