package ratel_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel"
)

type staticExecutor struct {
	ratel.Executor
	exec func(context.Context, string, ...any) (pgconn.CommandTag, error)
	copy func(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error)
}

func (e staticExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.exec(ctx, sql, args...)
}

func (e staticExecutor) CopyFrom(
	ctx context.Context, table pgx.Identifier, columns []string, rows pgx.CopyFromSource,
) (int64, error) {
	return e.copy(ctx, table, columns, rows)
}

func TestExec(t *testing.T) {
	t.Parallel()

	offline := errors.New("offline")
	unique := &pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_email_key"}

	for _, tc := range []struct {
		name      string
		statement ratel.Statement
		failure   error
		want      int64
		check     func(error) bool
	}{
		{
			name: "rows", statement: ratel.Statement{SQL: "DELETE FROM app.users", Args: []any{1}}, want: 3,
			check: func(err error) bool { return err == nil },
		},
		{
			name: "constraint", statement: ratel.Statement{SQL: "INSERT"}, failure: unique,
			check: func(err error) bool {
				return errors.Is(err, &ratel.ConstraintError{Kind: ratel.ConstraintUnique, Constraint: "users_email_key"})
			},
		},
		{
			name: "technical", statement: ratel.Statement{SQL: "INSERT"}, failure: offline,
			check: func(err error) bool { return errors.Is(err, offline) },
		},
		{name: "empty", check: func(err error) bool { return errors.Is(err, ratel.ErrInvalidQuery) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := staticExecutor{exec: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				if sql != tc.statement.SQL || len(args) == 0 || args[0] != pgx.QueryExecModeExec {
					t.Fatalf("exec(%q, %v)", sql, args)
				}

				return pgconn.NewCommandTag("DELETE 3"), tc.failure
			}}
			database := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeExec))

			got, err := database.Exec(t.Context(), tc.statement)
			if !tc.check(err) || (err == nil && got != tc.want) {
				t.Fatalf("Exec() = %d, %v", got, err)
			}
		})
	}
}

func TestCopyFrom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		rows    []writeInsert
		columns []string
		values  [][]any
		failure error
	}{
		{
			name: "required only", rows: []writeInsert{newWriteInsert("a"), newWriteInsert("b")},
			columns: []string{"email"}, values: [][]any{{"a"}, {"b"}},
		},
		{
			name: "same optional", rows: []writeInsert{withID("a", 1), withID("b", 2)},
			columns: []string{"email", "id"}, values: [][]any{{"a", int64(1)}, {"b", int64(2)}},
		},
		{
			name: "mixed optional", rows: []writeInsert{withID("a", 1), newWriteInsert("b")},
			failure: ratel.ErrMixedCopyColumns,
		},
		{name: "invalid insert", rows: []writeInsert{newWriteInsert("a"), {}}, failure: ratel.ErrInvalidInsert},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := staticExecutor{copy: func(
				_ context.Context, table pgx.Identifier, columns []string, source pgx.CopyFromSource,
			) (int64, error) {
				var values [][]any

				for source.Next() {
					row, err := source.Values()
					if err != nil {
						return 0, err
					}

					values = append(values, slices.Clone(row))
				}

				if source.Err() != nil {
					return 0, source.Err()
				}

				if !slices.Equal(table, pgx.Identifier{"app", "users"}) || !slices.Equal(columns, tc.columns) ||
					len(values) != len(tc.values) {
					t.Fatalf("copy(%v, %v, %v)", table, columns, values)
				}

				return int64(len(values)), nil
			}}
			database := ratel.New(executor)

			got, err := database.CopyFrom(t.Context(), writeDescriptor(), slices.Values(tc.rows))
			if !errors.Is(err, tc.failure) || (err == nil && got != int64(len(tc.values))) {
				t.Fatalf("CopyFrom() = %d, %v, want %v", got, err, tc.failure)
			}
		})
	}
}

func TestBwStaticInvalid(t *testing.T) {
	t.Parallel()

	executor := staticExecutor{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			t.Fatal("exec reached the executor")

			return pgconn.CommandTag{}, nil
		},
		copy: func(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
			t.Fatal("copy reached the executor")

			return 0, nil
		},
	}
	database := ratel.New(executor)
	source := pgx.CopyFromRows([][]any{{"a"}})
	unvalidated := ratel.NewWriteTable[readUser, writeInsert](userDescriptor().Table, nil)
	empty := ratel.NewWriteTable(ratel.NewTable[readUser]("app", "users", nil, scanReadUser),
		func(writeInsert) bool { return true })

	var missing *ratel.DB

	for name, run := range map[string]func() error{
		"exec without a database": func() error {
			_, err := missing.Exec(t.Context(), ratel.Statement{SQL: "DELETE FROM app.users"})

			return err
		},
		"copy without a database": func() error {
			_, err := missing.Copy(t.Context(), pgx.Identifier{"app", "users"}, []string{"email"}, source)

			return err
		},
		"copy into an unqualified table": func() error {
			_, err := database.Copy(t.Context(), pgx.Identifier{"users"}, []string{"email"}, source)

			return err
		},
		"copy without columns": func() error {
			_, err := database.Copy(t.Context(), pgx.Identifier{"app", "users"}, nil, source)

			return err
		},
		"copy without rows": func() error {
			_, err := database.Copy(t.Context(), pgx.Identifier{"app", "users"}, []string{"email"}, nil)

			return err
		},
		"copy from without a database": func() error {
			_, err := missing.CopyFrom(t.Context(), writeDescriptor(), slices.Values([]writeInsert{newWriteInsert("a")}))

			return err
		},
		"copy from without a sequence": func() error {
			_, err := database.CopyFrom(t.Context(), writeDescriptor(), nil)

			return err
		},
		"copy from a table without insert validation": func() error {
			_, err := database.CopyFrom(t.Context(), unvalidated, slices.Values([]writeInsert{newWriteInsert("a")}))

			return err
		},
		"copy from a table without columns": func() error {
			_, err := database.CopyFrom(t.Context(), empty, slices.Values([]writeInsert{newWriteInsert("a")}))

			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := run(); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestBwCopy(t *testing.T) {
	t.Parallel()

	offline := errors.New("offline")
	unique := &pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_email_key"}

	for _, tc := range []struct {
		name    string
		count   int64
		failure error
		check   func(error) bool
	}{
		{name: "copied", count: 2, check: func(err error) bool { return err == nil }},
		{name: "technical", failure: offline, check: func(err error) bool { return errors.Is(err, offline) }},
		{
			name: "constraint", failure: unique,
			check: func(err error) bool {
				return errors.Is(err, &ratel.ConstraintError{
					Kind: ratel.ConstraintUnique, Schema: "app", Table: "users", Constraint: "users_email_key",
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := staticExecutor{copy: func(
				_ context.Context, table pgx.Identifier, columns []string, _ pgx.CopyFromSource,
			) (int64, error) {
				if !slices.Equal(table, pgx.Identifier{"app", "users"}) || !slices.Equal(columns, []string{"email"}) {
					t.Fatalf("copy(%v, %v)", table, columns)
				}

				return tc.count, tc.failure
			}}

			got, err := ratel.New(executor).Copy(t.Context(), pgx.Identifier{"app", "users"}, []string{"email"},
				pgx.CopyFromRows([][]any{{"a"}, {"b"}}))
			if !tc.check(err) || got != tc.count {
				t.Fatalf("Copy() = %d, %v", got, err)
			}
		})
	}
}

func BenchmarkBwCopy(b *testing.B) {
	executor := staticExecutor{copy: func(
		_ context.Context, _ pgx.Identifier, _ []string, source pgx.CopyFromSource,
	) (int64, error) {
		var count int64
		for source.Next() {
			count++
		}

		return count, source.Err()
	}}
	database := ratel.New(executor)
	rows := make([][]any, 1000)

	for index := range rows {
		rows[index] = []any{"a"}
	}

	for b.Loop() {
		if _, err := database.Copy(b.Context(), pgx.Identifier{"app", "users"}, []string{"email"},
			pgx.CopyFromRows(rows)); err != nil {
			b.Fatal(err)
		}
	}
}

func withID(email string, id int64) writeInsert {
	row := newWriteInsert(email)
	row.ID = ratel.OptFrom(id)

	return row
}

func BenchmarkExec(b *testing.B) {
	executor := staticExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}}
	database := ratel.New(executor)
	statement := ratel.Statement{SQL: "UPDATE app.users SET email = $1", Args: []any{"a"}}

	for b.Loop() {
		if _, err := database.Exec(b.Context(), statement); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCopyFrom(b *testing.B) {
	executor := staticExecutor{copy: func(
		_ context.Context, _ pgx.Identifier, _ []string, source pgx.CopyFromSource,
	) (int64, error) {
		var count int64
		for source.Next() {
			if _, err := source.Values(); err != nil {
				return count, err
			}

			count++
		}

		return count, source.Err()
	}}
	database := ratel.New(executor)
	rows := slices.Repeat([]writeInsert{newWriteInsert("a")}, 1000)

	for b.Loop() {
		if _, err := database.CopyFrom(b.Context(), writeDescriptor(), slices.Values(rows)); err != nil {
			b.Fatal(err)
		}
	}
}
