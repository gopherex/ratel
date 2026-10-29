package ratel

import (
	"context"
	"iter"
	"slices"

	"github.com/jackc/pgx/v5"
)

// arguments prepends the effective query exec mode, the per-call option
// winning over WithExecMode, to the statement's arguments.
func (db *DB) arguments(options []CallOption, args []any) []any {
	settings := callSettings{mode: db.execMode}
	for _, option := range options {
		settings = option(settings)
	}

	if settings.mode.Set {
		return append(append(make([]any, 0, len(args)+1), settings.mode.V), args...)
	}

	return args
}

// Exec runs a static statement, as generated for :exec and :execrows queries,
// and returns the number of affected rows. An integrity violation is a
// *ConstraintError.
func (db *DB) Exec(ctx context.Context, statement Statement, options ...CallOption) (int64, error) {
	if db == nil || statement.SQL == "" {
		return 0, ErrInvalidQuery
	}

	tag, err := db.executor.Exec(ctx, statement.SQL, db.arguments(options, statement.Args)...)
	if err != nil {
		return 0, mapWriteError(err, Table[struct{}]{})
	}

	return tag.RowsAffected(), nil
}

// Copy streams rows into columns of a schema-qualified table through COPY, as
// generated for :copyfrom queries, and returns the number of copied rows.
func (db *DB) Copy(
	ctx context.Context, table pgx.Identifier, columns []string, rows pgx.CopyFromSource,
) (int64, error) {
	if db == nil || len(table) != 2 || len(columns) == 0 || rows == nil {
		return 0, ErrInvalidQuery
	}

	count, err := db.executor.CopyFrom(ctx, table, columns, rows)
	if err != nil {
		return 0, mapWriteError(err, Table[struct{}]{schema: table[0], name: table[1]})
	}

	return count, nil
}

// ErrMixedCopyColumns rejects COPY rows that set different optional
// columns: COPY sends one column list, and an unset column takes its default.
const ErrMixedCopyColumns refusal = "ratel: copy rows set different columns"

// CopyFrom streams inserts into a table through COPY without buffering them.
// The first row's set columns are copied; every row must set the same ones,
// so no optional value is silently dropped.
func (db *DB) CopyFrom[M, I any](ctx context.Context, table Writable[M, I], rows iter.Seq[I]) (int64, error) {
	write := table.ratelWrite()
	if db == nil || !write.valid() || write.validInsert == nil || rows == nil {
		return 0, ErrInvalidQuery
	}

	next, stop := iter.Pull(rows)
	defer stop()

	first, ok := next()
	if !ok {
		return 0, nil
	}

	source := &copySource[I]{fields: write.fields, valid: write.validInsert, next: next, pending: &first}
	columns := make([]string, 0, len(write.fields))

	for _, field := range write.fields {
		if _, set := field.Value(first); set {
			columns = append(columns, field.Name)
		}
	}

	source.columns = columns

	count, err := db.Copy(ctx, pgx.Identifier{write.schema, write.name}, columns, source)
	if source.err != nil {
		return 0, source.err
	}

	return count, err
}

// copySource adapts a pulled sequence of inserts to pgx.CopyFromSource.
type copySource[I any] struct {
	fields  []InsertField[I]
	columns []string
	valid   func(I) bool
	next    func() (I, bool)
	pending *I
	values  []any
	err     error
}

func (s *copySource[I]) Next() bool {
	var (
		row     I
		present bool
	)

	if s.pending != nil {
		row, present, s.pending = *s.pending, true, nil
	} else {
		row, present = s.next()
	}

	if !present || s.err != nil {
		return false
	}

	if !s.valid(row) {
		s.err = ErrInvalidInsert

		return false
	}

	s.values = s.values[:0]

	for _, field := range s.fields {
		value, set := field.Value(row)
		if set != slices.Contains(s.columns, field.Name) {
			s.err = ErrMixedCopyColumns

			return false
		}

		if set {
			s.values = append(s.values, value)
		}
	}

	return true
}

func (s *copySource[I]) Values() ([]any, error) { return s.values, nil }

func (s *copySource[I]) Err() error { return s.err }
