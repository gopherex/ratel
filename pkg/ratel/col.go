package ratel

import "slices"

// ReadCol is a generated, model-typed column the database computes: a
// generated column or an identity GENERATED ALWAYS. It filters, orders,
// joins and names a conflict target like any column, but it has no
// assignment and cannot be an upsert update, so a write PostgreSQL would
// refuse ("cannot insert a non-DEFAULT value") does not compile.
type ReadCol[M, V any] struct {
	name  string
	codec *Codec[V]
}

// NewReadCol constructs a computed column from its unqualified PostgreSQL name.
func NewReadCol[M, V any](name string) ReadCol[M, V] { return ReadCol[M, V]{name: name} }

// NullReadCol is a nullable computed column.
type NullReadCol[M, V any] struct{ ReadCol[M, V] }

// NewNullReadCol constructs a nullable computed column.
func NewNullReadCol[M, V any](name string) NullReadCol[M, V] {
	return NullReadCol[M, V]{ReadCol: NewReadCol[M, V](name)}
}

// Col is a generated, model-typed column. Its value type controls operator
// inputs; it reads like a ReadCol and also assigns.
type Col[M, V any] struct{ ReadCol[M, V] }

// NewCol constructs a column from its unqualified PostgreSQL name.
func NewCol[M, V any](name string) Col[M, V] { return Col[M, V]{ReadCol: NewReadCol[M, V](name)} }

// NullCol adds SQL NULL predicates without changing comparison argument types.
type NullCol[M, V any] struct{ Col[M, V] }

// NewNullCol constructs a nullable generated column.
func NewNullCol[M, V any](name string) NullCol[M, V] {
	return NullCol[M, V]{Col: NewCol[M, V](name)}
}

func (c ReadCol[M, V]) compare(operator string, value V) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.column(c.name)
		writer.WriteString(operator)
		writer.param(c.arg(value))
	}}
}

// Eq compares with SQL equality.
func (c ReadCol[M, V]) Eq(value V) Cond[M] { return c.compare(" = ", value) }

// Ne compares with SQL inequality.
func (c ReadCol[M, V]) Ne(value V) Cond[M] { return c.compare(" <> ", value) }

// Lt compares with SQL less-than.
func (c ReadCol[M, V]) Lt(value V) Cond[M] { return c.compare(" < ", value) }

// Lte compares with SQL less-than-or-equal.
func (c ReadCol[M, V]) Lte(value V) Cond[M] { return c.compare(" <= ", value) }

// Gt compares with SQL greater-than.
func (c ReadCol[M, V]) Gt(value V) Cond[M] { return c.compare(" > ", value) }

// Gte compares with SQL greater-than-or-equal.
func (c ReadCol[M, V]) Gte(value V) Cond[M] { return c.compare(" >= ", value) }

// Like compares using the PostgreSQL LIKE operator.
func (c ReadCol[M, V]) Like(value V) Cond[M] { return c.compare(" LIKE ", value) }

// NotLike negates the PostgreSQL LIKE operator.
func (c ReadCol[M, V]) NotLike(value V) Cond[M] { return c.compare(" NOT LIKE ", value) }

// ILike compares using the PostgreSQL case-insensitive ILIKE operator.
func (c ReadCol[M, V]) ILike(value V) Cond[M] { return c.compare(" ILIKE ", value) }

// Between compares an inclusive range.
func (c ReadCol[M, V]) Between(lower, upper V) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.column(c.name)
		writer.WriteString(" BETWEEN ")
		writer.param(c.arg(lower))
		writer.WriteString(" AND ")
		writer.param(c.arg(upper))
	}}
}

// In tests membership. An empty set is always false.
func (c ReadCol[M, V]) In(values ...V) Cond[M] { return c.membership(false, values) }

// NotIn tests non-membership. An empty set is always true.
func (c ReadCol[M, V]) NotIn(values ...V) Cond[M] { return c.membership(true, values) }

func (c ReadCol[M, V]) membership(negate bool, values []V) Cond[M] {
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
			writer.param(c.arg(value))
		}
		writer.WriteByte(')')
	}}
}

// IsNull tests SQL NULL.
func (c NullCol[M, V]) IsNull() Cond[M] { return c.nullCondition(" IS NULL") }

// IsNotNull tests SQL non-NULL.
func (c NullCol[M, V]) IsNotNull() Cond[M] { return c.nullCondition(" IS NOT NULL") }

// IsNull tests SQL NULL.
func (c NullReadCol[M, V]) IsNull() Cond[M] { return c.nullCondition(" IS NULL") }

// IsNotNull tests SQL non-NULL.
func (c NullReadCol[M, V]) IsNotNull() Cond[M] { return c.nullCondition(" IS NOT NULL") }

func (c ReadCol[M, V]) nullCondition(operator string) Cond[M] {
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
func (c ReadCol[M, V]) Asc() Order[M] { return Order[M]{name: c.name} }

// Desc orders a column descending, with PostgreSQL's default NULLS FIRST.
func (c ReadCol[M, V]) Desc() Order[M] { return Order[M]{name: c.name, descending: true} }

// Column is implemented by generated nullable and non-null columns.
type Column[M, V any] interface{ ratelColumn() columnRef[M, V] }

type columnRef[M, V any] struct{ name string }

func (c ReadCol[M, V]) ratelColumn() columnRef[M, V] { return columnRef[M, V]{name: c.name} }

// EqCol creates a typed join predicate. Joins assign independent source aliases.
func (c ReadCol[M, V]) EqCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " = ")
}

// NeCol compares columns using SQL inequality.
func (c ReadCol[M, V]) NeCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " <> ")
}

// LtCol compares columns using SQL less-than.
func (c ReadCol[M, V]) LtCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " < ")
}

// LteCol compares columns using SQL less-than-or-equal.
func (c ReadCol[M, V]) LteCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " <= ")
}

// GtCol compares columns using SQL greater-than.
func (c ReadCol[M, V]) GtCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " > ")
}

// GteCol compares columns using SQL greater-than-or-equal.
func (c ReadCol[M, V]) GteCol[N any](other Column[N, V]) Cond2[M, N] {
	return c.compareColumn(other, " >= ")
}

func (c ReadCol[M, V]) compareColumn[N any](other Column[N, V], operator string) Cond2[M, N] {
	right := other.ratelColumn().name

	return Cond2[M, N]{expr: func(writer *sqlWriter, leftAlias, rightAlias string) {
		writer.qualifiedColumn(leftAlias, c.name)
		writer.WriteString(operator)
		writer.qualifiedColumn(rightAlias, right)
	}}
}
