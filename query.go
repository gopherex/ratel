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

// stmt is rendered SQL with its ordered arguments.
type stmt struct {
	SQL  string
	Args []any
}

// result is the written statement; one without parameters has nil Args.
func (w *sqlWriter) result() stmt {
	if len(w.args) == 0 {
		return stmt{SQL: w.String()}
	}

	return stmt{SQL: w.String(), Args: w.args}
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
	source   source[M]
	where    Cond[M]
	hasWhere bool
	lock     lockMode // FOR UPDATE / FOR SHARE clause
	wait     waitMode // SKIP LOCKED / NOWAIT; requires a lock
	orders   []Order[M]
	limit    Opt[int]
	offset   Opt[int]
	after    []any // keyset values matching orders
	joins    []func(writer *sqlWriter, alias string) error
}

// lockMode is a row-locking clause. It is a byte, not its SQL text, to keep
// Select small: db.Run copies the SELECT into its runner.
type lockMode uint8

const (
	lockNone lockMode = iota
	lockUpdate
	lockShare
)

func (m lockMode) sql() string {
	switch m {
	case lockUpdate:
		return " FOR UPDATE"
	case lockShare:
		return " FOR SHARE"
	case lockNone:
		return ""
	default:
		return ""
	}
}

// waitMode is the wait policy of a row lock.
type waitMode uint8

const (
	waitDefault waitMode = iota
	waitSkipLocked
	waitNoWait
)

func (m waitMode) sql() string {
	switch m {
	case waitSkipLocked:
		return " SKIP LOCKED"
	case waitNoWait:
		return " NOWAIT"
	case waitDefault:
		return ""
	default:
		return ""
	}
}

// From starts a SELECT over any source: a generated descriptor, a static
// query, or another SELECT. The SELECT is a value; db.Run executes it.
func From[M any](from Source[M]) Select[M] {
	return Select[M]{source: from.ratelSource()}
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

// Build renders the query and its numbered PostgreSQL arguments without
// executing it or contacting a database.
func (q Select[M]) Build() (string, []any, error) {
	rendered, err := q.render()
	if err != nil {
		return "", nil, err
	}

	if q.verbatim() {
		// The caller owns the returned arguments; a static query keeps its own.
		rendered.Args = slices.Clone(rendered.Args)
	}

	return rendered.SQL, rendered.Args, nil
}

// render renders the query; a static query's arguments are shared, for
// execution, which does not modify them.
func (q *Select[M]) render() (stmt, error) {
	if q.verbatim() {
		if q.source.static.SQL == "" {
			return stmt{}, ErrInvalidQuery
		}

		return *q.source.static, nil
	}

	if q.source.table.tableMeta == nil {
		return stmt{}, ErrInvalidQuery
	}

	if q.source.render == nil && q.source.table.selectSQL != "" && q.plain() {
		return stmt{SQL: q.source.table.selectSQL}, nil
	}

	writer := newSQLWriter()
	writer.Grow(len(q.source.table.selectSQL))

	if err := q.appendSQL(writer); err != nil {
		return stmt{}, err
	}

	if writer.invalid {
		return stmt{}, ErrInvalidQuery
	}

	return writer.result(), nil
}

// plain reports a SELECT without added clauses.
func (q *Select[M]) plain() bool {
	return !q.hasWhere && len(q.orders) == 0 && !q.limit.Set && !q.offset.Set && q.after == nil &&
		q.lock == lockNone && q.wait == waitDefault && len(q.joins) == 0
}

// verbatim reports a plain SELECT over a static query, which runs the
// query's own statement.
func (q *Select[M]) verbatim() bool { return q.source.static != nil && q.plain() }

func (q *Select[M]) appendSQL(writer *sqlWriter) error {
	if !q.source.table.valid() || q.limit.V < 0 || q.offset.V < 0 ||
		(q.after != nil && len(q.after) != len(q.orders)) || (q.wait != waitDefault && q.lock == lockNone) {
		return ErrInvalidQuery
	}

	if err := q.appendFrom(writer); err != nil {
		return err
	}

	q.appendWhere(writer)
	q.appendOrder(writer)
	q.appendPagination(writer)
	writer.WriteString(q.lock.sql())
	writer.WriteString(q.wait.sql())

	return nil
}

// appendFrom renders the projection, the source as "q" and the joins.
func (q *Select[M]) appendFrom(writer *sqlWriter) error {
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
func (q *Select[M]) appendWhere(writer *sqlWriter) {
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

func (q *Select[M]) appendOrder(writer *sqlWriter) {
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

func (q *Select[M]) appendPagination(writer *sqlWriter) {
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
