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
	table  Table[M]
	render func(*sqlWriter) error
	// static is a static query's own statement. A SELECT without clauses runs
	// it verbatim, so DML with RETURNING, which cannot be a subquery, runs too.
	static *Statement
}

// Table describes an immutable, schema-qualified result and its generated scanner.
type Table[M any] struct {
	schema      string
	name        string
	columns     []string
	scan        Scanner[M]
	keys        []string
	keyValues   func(M) []any
	values      func(M) []any // every column's model value, in column order
	constraints []ConstraintError
	projection  string
	selectSQL   string
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
	table := Table[M]{schema: schema, name: name, columns: slices.Clone(columns), scan: scan}
	for _, option := range options {
		table = option(table)
	}

	table.projection = table.renderProjection()
	table = table.withSchema(table.schema)

	return table
}

func (t Table[M]) renderProjection() string {
	writer := newSQLWriter()

	for index, column := range t.columns {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.column(column)
	}

	return writer.String()
}

func (t Table[M]) withSchema(schema string) Table[M] {
	t.schema = schema
	t.selectSQL = ""

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
	return source[M]{table: t}
}

func (t Table[M]) render(writer *sqlWriter) error {
	if !validIdentifier(t.schema) || !validIdentifier(t.name) {
		return ErrInvalidQuery
	}

	writer.qualifiedColumn(t.schema, t.name)

	return nil
}

func (t Table[M]) valid() bool {
	if len(t.columns) == 0 || t.scan == nil {
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

// Query is a generated static SELECT, bound to its arguments.
type Query[M any] struct {
	table Table[M]
	sql   string
	args  []any
}

// NewQuery binds a static SELECT whose output columns match the generated scanner.
// Its parameters are contiguous $1..$n; SQL is trusted, generated PostgreSQL SQL.
func NewQuery[M any](columns []string, scan Scanner[M], sql string, args ...any) Query[M] {
	return Query[M]{table: NewTable("", "", columns, scan), sql: sql, args: slices.Clone(args)}
}

func (q Query[M]) ratelSource() source[M] {
	return source[M]{table: q.table, static: &Statement{SQL: q.sql, Args: q.args}, render: func(writer *sqlWriter) error {
		if q.sql == "" || len(writer.args) != 0 {
			return ErrInvalidQuery
		}

		writer.WriteByte('(')
		writer.WriteString(q.sql)
		writer.WriteByte(')')
		writer.args = append(writer.args, q.args...)

		return nil
	}}
}
