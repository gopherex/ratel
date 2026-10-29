package ratel

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
)

// ConstraintKind identifies a PostgreSQL integrity-constraint refusal.
type ConstraintKind string

const (
	ConstraintUnique     ConstraintKind = "unique"
	ConstraintForeignKey ConstraintKind = "foreign_key"
	ConstraintCheck      ConstraintKind = "check"
	ConstraintNotNull    ConstraintKind = "not_null"
	ConstraintExclusion  ConstraintKind = "exclusion"
)

// ConstraintError is an expected refusal. Generated sentinel values specify
// Kind, Schema, Table and Constraint; errors.Is matches their nonempty fields.
// Cause preserves the original pgconn.PgError without copying its sensitive text.
type ConstraintError struct {
	Kind       ConstraintKind
	Schema     string
	Table      string
	Constraint string
	Columns    []string
	Cause      error
}

func (e *ConstraintError) Error() string {
	return fmt.Sprintf("ratel: %s constraint %s.%s.%s", e.Kind, e.Schema, e.Table, e.Constraint)
}

// Unwrap preserves the driver error for errors.Is and errors.As.
func (e *ConstraintError) Unwrap() error { return e.Cause }

// Is matches generated constraint sentinel metadata, not driver error text.
func (e *ConstraintError) Is(target error) bool {
	switch expected := target.(type) {
	case *ConstraintError:
		return expected != nil && expected.Kind != "" && e.Kind == expected.Kind &&
			(expected.Schema == "" || e.Schema == expected.Schema) &&
			(expected.Table == "" || e.Table == expected.Table) &&
			(expected.Constraint == "" || e.Constraint == expected.Constraint)
	default:
		return false
	}
}

// Constraints supplies generated column metadata for named constraints.
func Constraints[M any](constraints ...ConstraintError) TableOption[M] {
	copied := slices.Clone(constraints)
	for index := range copied {
		copied[index].Columns = slices.Clone(copied[index].Columns)
	}

	return func(table Table[M]) Table[M] {
		table.constraints = copied

		return table
	}
}

// writeTarget is what write-error mapping needs of a table: its identity and
// constraint column metadata. The zero value is a static statement's target.
type writeTarget struct {
	schema      string
	name        string
	constraints []ConstraintError
}

func (t Table[M]) target() writeTarget {
	target := writeTarget{schema: t.schema, name: t.name}
	if t.tableMeta != nil {
		target.constraints = t.constraints
	}

	return target
}

func mapWriteError(err error, table writeTarget) error {
	if err == nil {
		return nil
	}

	var driver *pgconn.PgError
	if !errors.As(err, &driver) {
		return fmt.Errorf("ratel: write: %w", err)
	}

	kind := constraintKind(driver.Code)
	if kind == "" {
		return fmt.Errorf("ratel: write: %w", err)
	}

	refusal := &ConstraintError{
		Kind: kind, Schema: driver.SchemaName, Table: driver.TableName,
		Constraint: driver.ConstraintName, Cause: err,
	}
	if driver.ColumnName != "" {
		refusal.Columns = []string{driver.ColumnName}
	}

	for _, constraint := range table.constraints {
		if constraint.Constraint == driver.ConstraintName &&
			driver.SchemaName == table.schema && driver.TableName == table.name {
			refusal.Columns = slices.Clone(constraint.Columns)

			break
		}
	}

	return refusal
}

func constraintKind(code string) ConstraintKind {
	switch code {
	case "23505":
		return ConstraintUnique
	case "23503":
		return ConstraintForeignKey
	case "23514":
		return ConstraintCheck
	case "23502":
		return ConstraintNotNull
	case "23P01":
		return ConstraintExclusion
	default:
		return ""
	}
}
