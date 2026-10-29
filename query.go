package ratel

import (
	"slices"
	"strconv"
	"strings"
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
	alias   string // source alias model columns render with; empty is "q"
	depth   int    // nesting of correlated subqueries, for unique aliases
	// inline backs args for a typical statement, in the writer's own
	// allocation.
	inline [writerParameters]any
}

// Initial capacities of a statement being written: room for a typical
// SELECT and its parameters, so the buffers rarely grow (BenchmarkSelect).
const (
	writerBytes      = 128
	writerParameters = 4
)

// statement is the written statement; one without parameters has nil Args.
func (w *sqlWriter) statement() Statement {
	if len(w.args) == 0 {
		return Statement{SQL: w.String()}
	}

	return Statement{SQL: w.String(), Args: w.args}
}

// newSQLWriter is a writer with room for a typical statement.
func newSQLWriter() *sqlWriter {
	writer := &sqlWriter{}
	writer.args = writer.inline[:0]
	writer.Grow(writerBytes)

	return writer
}

func (w *sqlWriter) param(value any) {
	w.args = append(w.args, value)
	w.WriteByte('$')
	w.WriteString(strconv.Itoa(len(w.args)))
}

func (w *sqlWriter) column(name string) { w.qualifiedColumn(w.source(), name) }

// source is the alias the current model's columns render with.
func (w *sqlWriter) source() string {
	if w.alias == "" {
		return "q"
	}

	return w.alias
}

func (w *sqlWriter) qualifiedColumn(alias, name string) {
	w.identifier(alias)
	w.WriteByte('.')
	w.identifier(name)
}

func (w *sqlWriter) identifier(name string) {
	if !validIdentifier(name) {
		w.invalid = true
	}

	w.WriteByte('"')

	for {
		index := strings.IndexByte(name, '"')
		if index < 0 {
			w.WriteString(name)

			break
		}

		w.WriteString(name[:index+1])
		w.WriteByte('"')

		name = name[index+1:]
	}

	w.WriteByte('"')
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
	after    []any  // keyset values matching orders
	lock     string // FOR UPDATE / FOR SHARE clause
	wait     string // SKIP LOCKED / NOWAIT; requires a lock
	joins    []func(writer *sqlWriter, alias string) error
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
	statement, err := q.statement()
	if err == nil && q.verbatim() {
		// The caller owns the returned arguments; a static query keeps its own.
		statement.Args = slices.Clone(statement.Args)
	}

	return statement, err
}

// statement renders the query; a static query's arguments are shared, for
// execution, which does not modify them.
func (q Select[M]) statement() (Statement, error) {
	if q.verbatim() {
		if q.source.static.SQL == "" {
			return Statement{}, ErrInvalidQuery
		}

		return *q.source.static, nil
	}

	if q.source.render == nil && q.source.table.selectSQL != "" && q.plain() {
		return Statement{SQL: q.source.table.selectSQL}, nil
	}

	writer := newSQLWriter()
	writer.Grow(len(q.source.table.selectSQL))

	if err := q.appendSQL(writer); err != nil {
		return Statement{}, err
	}

	if writer.invalid {
		return Statement{}, ErrInvalidQuery
	}

	return writer.statement(), nil
}

// plain reports a SELECT without added clauses.
func (q Select[M]) plain() bool {
	return !q.hasWhere && len(q.orders) == 0 && !q.limit.Set && !q.offset.Set && q.after == nil &&
		q.lock == "" && q.wait == "" && len(q.joins) == 0
}

// verbatim reports a plain SELECT over a static query, which runs the
// query's own statement.
func (q Select[M]) verbatim() bool { return q.source.static != nil && q.plain() }

func (q Select[M]) appendSQL(writer *sqlWriter) error {
	if !q.source.table.valid() || q.limit.V < 0 || q.offset.V < 0 ||
		(q.after != nil && len(q.after) != len(q.orders)) || (q.wait != "" && q.lock == "") {
		return ErrInvalidQuery
	}

	if err := q.appendFrom(writer); err != nil {
		return err
	}

	q.appendWhere(writer)
	q.appendOrder(writer)
	q.appendPagination(writer)
	writer.WriteString(q.lock)
	writer.WriteString(q.wait)

	return nil
}

// appendFrom renders the projection, the source as "q" and the joins.
func (q Select[M]) appendFrom(writer *sqlWriter) error {
	if q.source.render == nil {
		if q.source.table.selectSQL == "" {
			return ErrInvalidQuery
		}

		writer.WriteString(q.source.table.selectSQL)
	} else {
		writer.WriteString("SELECT ")
		writer.WriteString(q.source.table.projection)
		writer.WriteString(" FROM ")

		if err := q.source.render(writer); err != nil {
			return err
		}

		writer.WriteString(` AS "q"`)
	}

	for index, join := range q.joins {
		if err := join(writer, "j"+strconv.Itoa(index)); err != nil {
			return err
		}
	}

	return nil
}

// appendWhere conjoins the predicates and the keyset condition.
func (q Select[M]) appendWhere(writer *sqlWriter) {
	switch {
	case q.hasWhere && q.after != nil:
		writer.WriteString(" WHERE ")
		q.where.And(q.keyset()).appendSQL(writer)
	case q.hasWhere:
		writer.WriteString(" WHERE ")
		q.where.appendSQL(writer)
	case q.after != nil:
		writer.WriteString(" WHERE ")
		q.keyset().appendSQL(writer)
	default:
	}
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
