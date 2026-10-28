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
}

// Table describes an immutable, schema-qualified result and its generated scanner.
type Table[M any] struct {
	schema    string
	name      string
	columns   []string
	scan      Scanner[M]
	keys      []string
	keyValues func(M) []any
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

// NewTable constructs the descriptor embedded by generated table and view types.
func NewTable[M any](schema, name string, columns []string, scan Scanner[M], options ...TableOption[M]) Table[M] {
	table := Table[M]{schema: schema, name: name, columns: slices.Clone(columns), scan: scan}
	for _, option := range options {
		table = option(table)
	}

	return table
}

// InSchema returns table metadata for another schema. Generated descriptors wrap
// this method to preserve their typed column and relation fields.
func (t Table[M]) InSchema(schema string) Table[M] {
	t.schema = schema

	return t
}

func (t Table[M]) ratelSource() source[M] {
	return source[M]{table: t, render: func(writer *sqlWriter) error {
		if !validIdentifier(t.schema) || !validIdentifier(t.name) {
			return ErrInvalidQuery
		}

		writer.WriteString(pgx.Identifier{t.schema, t.name}.Sanitize())

		return nil
	}}
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
	return Query[M]{table: Table[M]{columns: slices.Clone(columns), scan: scan}, sql: sql, args: slices.Clone(args)}
}

func (q Query[M]) ratelSource() source[M] {
	return source[M]{table: q.table, render: func(writer *sqlWriter) error {
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
