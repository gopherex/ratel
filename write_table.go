package ratel

import (
	"slices"

	"github.com/jackc/pgx/v5"
)

// ErrInvalidInsert rejects an insert not constructed by its generated constructor.
const ErrInvalidInsert refusal = "ratel: invalid insert"

// InsertField is generated metadata for an insert column. Value reports whether
// the column is set. Type is the qualified PostgreSQL element type, without [].
// Values returns a native typed slice in input order, for scalar unnest. Its
// Go type must have a default pgx array mapping, including in exec/simple mode.
// Array marks array-valued columns, which use VALUES rather than scalar unnest.
type InsertField[I any] struct {
	Name   string
	Type   pgx.Identifier
	Value  func(I) (any, bool)
	Values func([]I) any
	Array  bool
}

// Writable is implemented only by generated physical-table descriptors.
// Read-only Table, Query and Select do not implement it.
type Writable[M, I any] interface {
	ratelWrite() WriteTable[M, I]
}

// WriteTable adds insert metadata to a read descriptor. Views embed only Table.
type WriteTable[M, I any] struct {
	Table[M]
	fields      []InsertField[I]
	validInsert func(I) bool
}

// NewWriteTable constructs generated metadata, copying its slices.
func NewWriteTable[M, I any](table Table[M], valid func(I) bool, fields ...InsertField[I]) WriteTable[M, I] {
	fields = slices.Clone(fields)
	for index := range fields {
		fields[index].Type = slices.Clone(fields[index].Type)
	}

	return WriteTable[M, I]{Table: table, fields: fields, validInsert: valid}
}

// InSchema preserves writable metadata for another schema.
func (t WriteTable[M, I]) InSchema(schema string) WriteTable[M, I] {
	t.Table = t.Table.InSchema(schema)

	return t
}

func (t WriteTable[M, I]) ratelWrite() WriteTable[M, I] { return t }

// WriteColumn is a model-typed column used in conflict targets and updates.
type WriteColumn[M any] interface{ ratelWriteColumn() string }

func (c Col[M, V]) ratelWriteColumn() string { return c.name }

// Assignment is a model-typed SET expression.
type Assignment[M any] struct {
	name  string
	value any
}

// To binds a non-null column value for SET.
func (c Col[M, V]) To(value V) Assignment[M] {
	return Assignment[M]{name: c.name, value: value}
}

// To binds either a value or SQL NULL for SET.
func (c NullCol[M, V]) To(value Null[V]) Assignment[M] {
	return Assignment[M]{name: c.name, value: value}
}
