package ratel

import (
	"slices"

	"github.com/jackc/pgx/v5"
)

const deleteOperation = "DELETE"

// Write is an immutable UPDATE or DELETE statement. WHERE or All is mandatory.
// It is a value: Build renders it, db.Run(write).Exec executes it, and
// Returning turns it into a row query.
type Write[M any] struct {
	table       Table[M]
	operation   string
	assignments []Assignment[M]
	where       Cond[M]
	hasWhere    bool
	all         bool
	invalid     bool    // key or model metadata is missing
	missing     refusal // returned when the write matches no row
}

// Update starts an UPDATE, including for a table without a primary key.
func (t WriteTable[M, I]) Update() Write[M] {
	return Write[M]{table: t.Table, operation: "UPDATE"}
}

// Delete starts a DELETE, including for a table without a primary key.
func (t WriteTable[M, I]) Delete() Write[M] {
	return Write[M]{table: t.Table, operation: deleteOperation}
}

// Set appends model-typed assignments. Duplicate and unknown columns are invalid.
func (q Write[M]) Set(assignments ...Assignment[M]) Write[M] {
	q.assignments = append(slices.Clone(q.assignments), assignments...)

	return q
}

// Where conjoins a predicate with earlier predicates.
func (q Write[M]) Where(condition Cond[M]) Write[M] {
	if q.hasWhere {
		q.where = q.where.And(condition)
	} else {
		q.where = condition
		q.hasWhere = true
	}

	return q
}

// All explicitly permits an operation without a WHERE clause.
func (q Write[M]) All() Write[M] {
	q.all = true

	return q
}

// Build renders the one parameterized statement, rejecting unsafe writes.
func (q Write[M]) Build() (string, []any, error) {
	rendered, err := q.render()

	return rendered.SQL, rendered.Args, err
}

func (q Write[M]) render() (stmt, error) {
	if q.invalid || !q.table.valid() || (!q.hasWhere && !q.all) ||
		(q.operation == deleteOperation && len(q.assignments) > 0) {
		return stmt{}, ErrInvalidQuery
	}

	writer := newSQLWriter()
	writer.WriteString(q.operation)

	if q.operation == deleteOperation {
		writer.WriteString(" FROM")
	}

	writer.WriteByte(' ')

	if err := q.table.render(writer); err != nil {
		return stmt{}, err
	}

	writer.WriteString(` AS "q"`)

	if q.operation == "UPDATE" {
		if err := q.appendAssignments(writer); err != nil {
			return stmt{}, err
		}
	}

	if q.hasWhere {
		writer.WriteString(" WHERE ")
		q.where.appendSQL(writer)
	}

	if writer.invalid {
		return stmt{}, ErrInvalidQuery
	}

	return writer.result(), nil
}

func (q Write[M]) appendAssignments(writer *sqlWriter) error {
	if len(q.assignments) == 0 {
		return ErrInvalidQuery
	}

	seen := make(map[string]bool, len(q.assignments))

	writer.WriteString(" SET ")

	for index, assignment := range q.assignments {
		if !slices.Contains(q.table.columns, assignment.name) || seen[assignment.name] {
			return ErrInvalidQuery
		}

		seen[assignment.name] = true

		if index > 0 {
			writer.WriteString(", ")
		}

		writer.WriteString(pgx.Identifier{assignment.name}.Sanitize())
		writer.WriteString(" = ")
		writer.param(assignment.value)
	}

	return nil
}

// Returning adds RETURNING of every model column and turns the write into a
// row query: db.Run gives it One, First, List and All, scanned by the
// descriptor, including defaults and BEFORE trigger values.
func (q Write[M]) Returning() WriteReturning[M] { return WriteReturning[M]{write: q} }

// WriteReturning is an UPDATE or DELETE with RETURNING, a row query.
type WriteReturning[M any] struct{ write Write[M] }

// Build renders the one parameterized statement with its RETURNING clause.
func (q WriteReturning[M]) Build() (string, []any, error) {
	rendered, err := q.render()

	return rendered.SQL, rendered.Args, err
}

func (q WriteReturning[M]) render() (stmt, error) {
	rendered, err := q.write.render()
	if err != nil {
		return stmt{}, err
	}

	return returning(rendered, q.write.table), nil
}

// returning appends RETURNING of every column of the table to a statement.
func returning[M any](rendered stmt, table Table[M]) stmt {
	rendered.SQL += " RETURNING " + table.projection

	return rendered
}

func (q Write[M]) execPlan() execPlan {
	rendered, err := q.render()

	return execPlan{
		plan:   plan{head: rendered, err: err, missing: q.missing},
		target: q.table.target(),
	}
}

func (q Write[M]) bind(db *DB) ExecRunner { return ExecRunner{db: db, plan: q.execPlan()} }

func (q WriteReturning[M]) rowsPlan() rowsPlan[M] {
	rendered, err := q.render()

	return rowsPlan[M]{
		plan:  plan{head: rendered, err: err, missing: q.write.missing},
		table: q.write.table, write: true,
	}
}

func (q WriteReturning[M]) bind(db *DB) RowRunner[M] { return RowRunner[M]{db: db, plan: q.rowsPlan()} }
