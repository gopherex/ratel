package ratel

import (
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Scanner is generated code that reads one model in descriptor column order.
type Scanner[M any] func(pgx.Row) (M, error)

// Source is a generated descriptor or a SELECT with the same result model.
// Embedding Table implements Source for generated descriptor structs.
type Source[M any] interface {
	ratelSource() source[M]
}

type source[M any] struct {
	table  *tableMeta[M]
	render func(*sqlWriter) error
	// static is a static query's own statement. A SELECT without clauses runs
	// it verbatim, so DML with RETURNING, which cannot be a subquery, runs too.
	static *stmt
}

// Table describes an immutable, schema-qualified result and its generated scanner.
type Table[M any] struct {
	// Everything sits behind one pointer, shared by every copy, so a Table
	// (and a Select over it) is a word to copy through db.Run. It is nil only
	// in a zero Table, which is never valid.
	*tableMeta[M]
}

// tableMeta is the immutable metadata of a Table.
type tableMeta[M any] struct {
	writeTarget
	scan       Scanner[M]
	projection string
	selectSQL  string
	columns    []string
	keys       []string
	keyValues  func(M) []any
	values     func(M) []any // every column's model value, in column order
}

// TableOption supplies generated table metadata.
type TableOption[M any] func(Table[M]) Table[M]

// PrimaryKey supplies ordered, non-null primary-key columns and their model values.
func PrimaryKey[M any](columns []string, values func(M) []any) TableOption[M] {
	keys := slices.Clone(columns)

	return func(table Table[M]) Table[M] {
		table.keys = keys
		table.keyValues = values

		return table
	}
}

// ModelValues supplies every column's value of a model, in the table's column
// order, so Save writes a model without reflection.
func ModelValues[M any](values func(M) []any) TableOption[M] {
	return func(table Table[M]) Table[M] {
		table.values = values

		return table
	}
}

// NewTable constructs the descriptor embedded by generated table and view types.
func NewTable[M any](schema, name string, columns []string, scan Scanner[M], options ...TableOption[M]) Table[M] {
	table := Table[M]{tableMeta: &tableMeta[M]{
		writeTarget: writeTarget{schema: schema, name: name}, scan: scan, columns: slices.Clone(columns),
	}}
	for _, option := range options {
		table = option(table)
	}

	table.projection = renderProjection(table.columns)
	table = table.withSchema(table.schema)

	return table
}

func renderProjection(columns []string) string {
	writer := newSQLWriter()

	for index, column := range columns {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.column(column)
	}

	return writer.String()
}

func (t Table[M]) withSchema(schema string) Table[M] {
	if t.tableMeta == nil {
		return t
	}

	meta := *t.tableMeta
	meta.schema = schema
	meta.selectSQL = ""
	t.tableMeta = &meta

	if t.valid() && validIdentifier(schema) && validIdentifier(t.name) {
		t.selectSQL = "SELECT " + t.projection + " FROM " + pgx.Identifier{schema, t.name}.Sanitize() + ` AS "q"`
	}

	return t
}

// InSchema returns table metadata for another schema. Generated descriptors wrap
// this method to preserve their typed column and relation fields.
func (t Table[M]) InSchema(schema string) Table[M] {
	return t.withSchema(schema)
}

func (t Table[M]) ratelSource() source[M] {
	return source[M]{table: t.tableMeta}
}

// Select starts a SELECT over the table's rows.
func (t Table[M]) Select() Select[M] {
	return Select[M]{source: t.ratelSource()}
}

func (t *tableMeta[M]) render(writer *sqlWriter) error {
	if t == nil || !validIdentifier(t.schema) || !validIdentifier(t.name) {
		return ErrInvalidQuery
	}

	writer.qualifiedColumn(t.schema, t.name)

	return nil
}

func (t *tableMeta[M]) valid() bool {
	if t == nil || len(t.columns) == 0 || t.scan == nil {
		return false
	}

	for _, column := range t.columns {
		if !validIdentifier(column) {
			return false
		}
	}

	return true
}

func validIdentifier(name string) bool {
	return name != "" && !strings.ContainsRune(name, 0)
}

// validIdentifiers reports whether every part of a qualified name is valid;
// an empty name is valid.
func validIdentifiers(parts []string) bool {
	for _, part := range parts {
		if !validIdentifier(part) {
			return false
		}
	}

	return true
}

// Query is a generated static row query, bound to its arguments. It is a
// value: Build renders it, db.Run runs its statement verbatim (so DML with
// RETURNING works) with the row verbs, and From starts a SELECT over its result.
type Query[M any] struct {
	statement *stmt
	columns   []string
	scan      Scanner[M]
}

// NewQuery binds a static row query whose output columns match the generated scanner.
// Its parameters are contiguous $1..$n; SQL is trusted, generated PostgreSQL SQL.
func NewQuery[M any](columns []string, scan Scanner[M], sql string, args ...any) Query[M] {
	return Query[M]{statement: newStaticStmt(sql, args), columns: slices.Clone(columns), scan: scan}
}

// Build returns the query's SQL and a copy of its arguments.
func (q Query[M]) Build() (string, []any, error) { return q.statement.buildStatic() }

func (q Query[M]) ratelSource() source[M] {
	if q.statement == nil {
		return source[M]{}
	}

	table := &tableMeta[M]{columns: q.columns, scan: q.scan, projection: renderProjection(q.columns)}

	return source[M]{table: table, static: q.statement, render: q.statement.appendSubquery}
}

func (s *stmt) appendSubquery(writer *sqlWriter) error {
	if s.SQL == "" || len(writer.args) != 0 {
		return ErrInvalidQuery
	}

	writer.WriteByte('(')
	writer.WriteString(s.SQL)
	writer.WriteByte(')')
	writer.args = append(writer.args, s.Args...)

	return nil
}

// Command is a generated static statement without result rows, as generated
// for :exec and :execrows queries. db.Run gives it a single verb, Exec.
type Command struct{ statement *stmt }

// NewCommand binds a static statement. Its parameters are contiguous $1..$n;
// SQL is trusted, generated PostgreSQL SQL.
func NewCommand(sql string, args ...any) Command {
	return Command{statement: newStaticStmt(sql, args)}
}

// Build returns the command's SQL and a copy of its arguments.
func (c Command) Build() (string, []any, error) { return c.statement.buildStatic() }

func (c Command) execPlan() execPlan { return execPlan{plan: c.statement.staticPlan()} }

func (c Command) bind(db *DB) ExecRunner { return ExecRunner{db: db, plan: c.execPlan()} }

func (q Query[M]) rowsPlan() rowsPlan[M] {
	return rowsPlan[M]{plan: q.statement.staticPlan(), scan: q.scan}
}

func (q Query[M]) bind(db *DB) RowRunner[M] { return RowRunner[M]{db: db, plan: q.rowsPlan()} }

func newStaticStmt(sql string, args []any) *stmt {
	return &stmt{SQL: sql, Args: slices.Clone(args)}
}

// staticPlan sends a static statement as it is; empty SQL cannot be sent.
func (s *stmt) staticPlan() plan {
	if s == nil || s.SQL == "" {
		return plan{err: ErrInvalidQuery}
	}

	return plan{head: *s}
}

// buildStatic renders a static statement: empty SQL is invalid, and the
// caller owns the returned arguments.
func (s *stmt) buildStatic() (string, []any, error) {
	if s == nil || s.SQL == "" {
		return "", nil, ErrInvalidQuery
	}

	return s.SQL, slices.Clone(s.Args), nil
}
