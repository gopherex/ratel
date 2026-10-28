package ratel

import "slices"

// Col is a generated, model-typed column. Its value type controls operator inputs.
type Col[M, V any] struct{ name string }

// NewCol constructs a column from its unqualified PostgreSQL name.
func NewCol[M, V any](name string) Col[M, V] { return Col[M, V]{name: name} }

// NullCol adds SQL NULL predicates without changing comparison argument types.
type NullCol[M, V any] struct{ Col[M, V] }

// NewNullCol constructs a nullable generated column.
func NewNullCol[M, V any](name string) NullCol[M, V] {
	return NullCol[M, V]{Col: NewCol[M, V](name)}
}

func (c Col[M, V]) compare(operator string, value V) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.column(c.name)
		writer.WriteString(operator)
		writer.param(value)
	}}
}

// Eq compares with SQL equality.
func (c Col[M, V]) Eq(value V) Cond[M] { return c.compare(" = ", value) }

// Ne compares with SQL inequality.
func (c Col[M, V]) Ne(value V) Cond[M] { return c.compare(" <> ", value) }

// Lt compares with SQL less-than.
func (c Col[M, V]) Lt(value V) Cond[M] { return c.compare(" < ", value) }

// Lte compares with SQL less-than-or-equal.
func (c Col[M, V]) Lte(value V) Cond[M] { return c.compare(" <= ", value) }

// Gt compares with SQL greater-than.
func (c Col[M, V]) Gt(value V) Cond[M] { return c.compare(" > ", value) }

// Gte compares with SQL greater-than-or-equal.
func (c Col[M, V]) Gte(value V) Cond[M] { return c.compare(" >= ", value) }

// Like compares using the PostgreSQL LIKE operator.
func (c Col[M, V]) Like(value V) Cond[M] { return c.compare(" LIKE ", value) }

// NotLike negates the PostgreSQL LIKE operator.
func (c Col[M, V]) NotLike(value V) Cond[M] { return c.compare(" NOT LIKE ", value) }

// ILike compares using the PostgreSQL case-insensitive ILIKE operator.
func (c Col[M, V]) ILike(value V) Cond[M] { return c.compare(" ILIKE ", value) }

// Between compares an inclusive range.
func (c Col[M, V]) Between(lower, upper V) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.column(c.name)
		writer.WriteString(" BETWEEN ")
		writer.param(lower)
		writer.WriteString(" AND ")
		writer.param(upper)
	}}
}

// In tests membership. An empty set is always false.
func (c Col[M, V]) In(values ...V) Cond[M] { return c.membership(false, values) }

// NotIn tests non-membership. An empty set is always true.
func (c Col[M, V]) NotIn(values ...V) Cond[M] { return c.membership(true, values) }

func (c Col[M, V]) membership(negate bool, values []V) Cond[M] {
	values = slices.Clone(values)

	return Cond[M]{expr: func(writer *sqlWriter) {
		if len(values) == 0 {
			writer.WriteString(map[bool]string{false: "FALSE", true: "TRUE"}[negate])

			return
		}

		writer.column(c.name)
		if negate {
			writer.WriteString(" NOT")
		}
		writer.WriteString(" IN (")
		for index, value := range values {
			if index > 0 {
				writer.WriteString(", ")
			}
			writer.param(value)
		}
		writer.WriteByte(')')
	}}
}

// IsNull tests SQL NULL.
func (c NullCol[M, V]) IsNull() Cond[M] { return c.nullCondition(" IS NULL") }

// IsNotNull tests SQL non-NULL.
func (c NullCol[M, V]) IsNotNull() Cond[M] { return c.nullCondition(" IS NOT NULL") }

func (c NullCol[M, V]) nullCondition(operator string) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.column(c.name)
		writer.WriteString(operator)
	}}
}

// Order is a model-typed ordering term.
type Order[M any] struct {
	name       string
	descending bool
}

// Asc orders a column ascending, with PostgreSQL's default NULLS LAST.
func (c Col[M, V]) Asc() Order[M] { return Order[M]{name: c.name} }

// Desc orders a column descending, with PostgreSQL's default NULLS FIRST.
func (c Col[M, V]) Desc() Order[M] { return Order[M]{name: c.name, descending: true} }

// Column is implemented by generated nullable and non-null columns.
type Column[M, V any] interface{ ratelColumn() columnRef[M, V] }

type columnRef[M, V any] struct{ name string }

func (c Col[M, V]) ratelColumn() columnRef[M, V] { return columnRef[M, V](c) }

// EqCol creates a typed join predicate. Joins assign independent source aliases.
func (c Col[M, V]) EqCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " = ")
}

// NeCol compares columns using SQL inequality.
func (c Col[M, V]) NeCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " <> ")
}

// LtCol compares columns using SQL less-than.
func (c Col[M, V]) LtCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " < ")
}

// LteCol compares columns using SQL less-than-or-equal.
func (c Col[M, V]) LteCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " <= ")
}

// GtCol compares columns using SQL greater-than.
func (c Col[M, V]) GtCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " > ")
}

// GteCol compares columns using SQL greater-than-or-equal.
func (c Col[M, V]) GteCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " >= ")
}

func (c Col[M, V]) compareColumn[N any](other Column[N, V], operator string) Cond2[M, N] {
	right := other.ratelColumn().name

	return Cond2[M, N]{expr: func(writer *sqlWriter, leftAlias, rightAlias string) {
		writer.qualifiedColumn(leftAlias, c.name)
		writer.WriteString(operator)
		writer.qualifiedColumn(rightAlias, right)
	}}
}
