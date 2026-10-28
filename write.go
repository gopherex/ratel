package ratel

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

const deleteOperation = "DELETE"

// Write is an immutable UPDATE or DELETE builder. WHERE or All is mandatory.
type Write[M any] struct {
	db          *DB
	table       Table[M]
	operation   string
	assignments []Assignment[M]
	where       Cond[M]
	hasWhere    bool
	all         bool
}

// Update starts an UPDATE, including for a table without a primary key.
func (db *DB) Update[M, I any](table Writable[M, I]) Write[M] {
	return Write[M]{db: db, table: table.ratelWrite().Table, operation: "UPDATE"}
}

// Delete starts a DELETE, including for a table without a primary key.
func (db *DB) Delete[M, I any](table Writable[M, I]) Write[M] {
	return Write[M]{db: db, table: table.ratelWrite().Table, operation: deleteOperation}
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

// SQL renders one parameterized statement, rejecting unsafe writes.
func (q Write[M]) SQL() (Statement, error) {
	if !q.table.valid() || (!q.hasWhere && !q.all) || (q.operation == deleteOperation && len(q.assignments) > 0) {
		return Statement{}, ErrInvalidQuery
	}

	writer := &sqlWriter{}
	writer.WriteString(q.operation)

	if q.operation == deleteOperation {
		writer.WriteString(" FROM")
	}

	writer.WriteByte(' ')

	if err := q.table.ratelSource().render(writer); err != nil {
		return Statement{}, err
	}

	writer.WriteString(` AS "q"`)

	if q.operation == "UPDATE" {
		if err := q.appendAssignments(writer); err != nil {
			return Statement{}, err
		}
	}

	if q.hasWhere {
		writer.WriteString(" WHERE ")
		q.where.appendSQL(writer)
	}

	if writer.invalid {
		return Statement{}, ErrInvalidQuery
	}

	return Statement{SQL: writer.String(), Args: writer.args}, nil
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

// Exec executes the write and returns affected rows.
func (q Write[M]) Exec(ctx context.Context, options ...CallOption) (int64, error) {
	statement, err := q.SQL()
	if err != nil {
		return 0, err
	}

	result, err := executeWrite(ctx, q.db, q.table, q.operation, []Statement{statement}, false, options)

	return result.count, err
}

// Returning executes the write and scans every returned row into the model.
func (q Write[M]) Returning(ctx context.Context, options ...CallOption) ([]M, error) {
	statement, err := q.SQL()
	if err != nil {
		return nil, err
	}

	result, err := executeWrite(ctx, q.db, q.table, q.operation, []Statement{statement}, true, options)

	return result.models, err
}
