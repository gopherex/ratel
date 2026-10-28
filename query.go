package ratel

import (
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type refusal string

func (e refusal) Error() string { return string(e) }

const (
	// ErrNotFound means that a single-row operation returned no rows.
	ErrNotFound refusal = "ratel: row not found"
	// ErrInvalidQuery means query metadata or pagination is invalid.
	ErrInvalidQuery refusal = "ratel: invalid query"
	// ErrMultipleRows means One found more than one result.
	ErrMultipleRows refusal = "ratel: more than one row"
)

type sqlWriter struct {
	strings.Builder
	args    []any
	invalid bool
}

func (w *sqlWriter) param(value any) {
	w.args = append(w.args, value)
	w.WriteByte('$')
	w.WriteString(strconv.Itoa(len(w.args)))
}

func (w *sqlWriter) column(name string) { w.qualifiedColumn("q", name) }

func (w *sqlWriter) qualifiedColumn(alias, name string) {
	if !validIdentifier(alias) || !validIdentifier(name) {
		w.invalid = true
	}

	w.WriteString(pgx.Identifier{alias, name}.Sanitize())
}

// Select is an immutable typed SELECT. Branches can be used concurrently.
type Select[M any] struct {
	db       *DB
	source   source[M]
	where    Cond[M]
	hasWhere bool
	orders   []Order[M]
	limit    Opt[int]
	offset   Opt[int]
}

// From starts a SELECT over a generated descriptor, static query, or another SELECT.
func (db *DB) From[M any](from Source[M]) Select[M] {
	return Select[M]{db: db, source: from.ratelSource()}
}

// Where adds a predicate, conjoined with earlier predicates.
func (q Select[M]) Where(condition Cond[M]) Select[M] {
	if q.hasWhere {
		q.where = q.where.And(condition)
	} else {
		q.where = condition
		q.hasWhere = true
	}

	return q
}

// OrderBy appends model-typed ordering terms.
func (q Select[M]) OrderBy(orders ...Order[M]) Select[M] {
	q.orders = append(slices.Clone(q.orders), orders...)

	return q
}

// Limit caps the result. Zero returns no rows; negative values are rejected.
func (q Select[M]) Limit(count int) Select[M] {
	q.limit = OptFrom(count)

	return q
}

// Offset skips rows. Negative values are rejected.
func (q Select[M]) Offset(count int) Select[M] {
	q.offset = OptFrom(count)

	return q
}

// Statement contains rendered SQL and its ordered PostgreSQL arguments.
type Statement struct {
	SQL  string
	Args []any
}

// SQL renders the query and numbered PostgreSQL arguments without executing it.
func (q Select[M]) SQL() (Statement, error) {
	writer := &sqlWriter{}
	if err := q.appendSQL(writer); err != nil {
		return Statement{}, err
	}

	if writer.invalid {
		return Statement{}, ErrInvalidQuery
	}

	return Statement{SQL: writer.String(), Args: writer.args}, nil
}

func (q Select[M]) appendSQL(writer *sqlWriter) error {
	if !q.source.table.valid() || q.source.render == nil || q.limit.V < 0 || q.offset.V < 0 {
		return ErrInvalidQuery
	}

	writer.WriteString("SELECT ")

	for index, column := range q.source.table.columns {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.column(column)
	}

	writer.WriteString(" FROM ")

	if err := q.source.render(writer); err != nil {
		return err
	}

	writer.WriteString(` AS "q"`)

	if q.hasWhere {
		writer.WriteString(" WHERE ")
		q.where.appendSQL(writer)
	}

	q.appendOrder(writer)
	q.appendPagination(writer)

	return nil
}

func (q Select[M]) appendOrder(writer *sqlWriter) {
	if len(q.orders) == 0 {
		return
	}

	writer.WriteString(" ORDER BY ")

	for index, order := range q.orders {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.column(order.name)

		if order.descending {
			writer.WriteString(" DESC")
		} else {
			writer.WriteString(" ASC")
		}
	}
}

func (q Select[M]) appendPagination(writer *sqlWriter) {
	if q.limit.Set {
		writer.WriteString(" LIMIT ")
		writer.param(q.limit.V)
	}

	if q.offset.Set {
		writer.WriteString(" OFFSET ")
		writer.param(q.offset.V)
	}
}

func (q Select[M]) ratelSource() source[M] {
	return source[M]{table: q.source.table, render: func(writer *sqlWriter) error {
		writer.WriteByte('(')
		if err := q.appendSQL(writer); err != nil {
			return err
		}
		writer.WriteByte(')')

		return nil
	}}
}
