package ratel_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel/pkg/ratel"
)

func TestConstraintErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		code string
		kind ratel.ConstraintKind
	}{
		{"23505", ratel.ConstraintUnique},
		{"23503", ratel.ConstraintForeignKey},
		{"23514", ratel.ConstraintCheck},
		{"23502", ratel.ConstraintNotNull},
		{"23P01", ratel.ConstraintExclusion},
		{"40001", ""},
		{"08006", ""},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()

			cause := &pgconn.PgError{
				Code: tc.code, SchemaName: "app", TableName: "users",
				ConstraintName: "named", Message: "secret",
			}
			executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
				return pgconn.CommandTag{}, cause
			}}
			users := writeDescriptor()
			columns := []string{"email"}
			users.WriteTable = ratel.NewWriteTable(ratel.NewTable("app", "users", []string{"id"}, scanReadUser,
				ratel.Constraints[readUser](ratel.ConstraintError{Constraint: "named", Columns: columns})),
				func(row writeInsert) bool { return row.valid })
			columns[0] = "mutated"

			_, err := ratel.New(executor).Run(users.Delete().All()).Exec(t.Context())
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause: %v", err)
			}

			var constraint *ratel.ConstraintError
			if tc.kind == "" {
				if errors.As(err, &constraint) {
					t.Fatalf("technical error mapped to refusal: %v", err)
				}

				return
			}

			if !errors.As(err, &constraint) || constraint.Kind != tc.kind || len(constraint.Columns) != 1 ||
				constraint.Columns[0] != "email" || strings.Contains(constraint.Error(), "secret") {
				t.Fatalf("%+v", err)
			}

			for _, sentinel := range []error{
				&ratel.ConstraintError{Kind: tc.kind},
				&ratel.ConstraintError{Kind: tc.kind, Schema: "app", Table: "users", Constraint: "named"},
			} {
				if !errors.Is(err, sentinel) {
					t.Fatalf("did not match %v", sentinel)
				}
			}

			for _, sentinel := range []error{
				&ratel.ConstraintError{Kind: tc.kind, Schema: "other"},
				&ratel.ConstraintError{Kind: tc.kind, Table: "other"},
				&ratel.ConstraintError{Kind: tc.kind, Constraint: "other"},
				&ratel.ConstraintError{}, errors.New("other"), (*ratel.ConstraintError)(nil),
			} {
				if errors.Is(err, sentinel) {
					t.Fatalf("unexpected match: %v", sentinel)
				}
			}
		})
	}
}

func TestBwConstraintColumn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		column  string
		columns []string
	}{
		{"driver reports the column", "email", []string{"email"}},
		{"driver reports none", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cause := &pgconn.PgError{Code: "23502", ColumnName: tc.column, TableName: "users"}
			executor := staticExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
				return pgconn.CommandTag{}, cause
			}}

			_, err := ratel.New(executor).Run(ratel.NewCommand("INSERT INTO app.users DEFAULT VALUES")).Exec(t.Context())

			var constraint *ratel.ConstraintError
			if !errors.As(err, &constraint) || constraint.Kind != ratel.ConstraintNotNull ||
				!slices.Equal(constraint.Columns, tc.columns) {
				t.Fatalf("Exec() = %+v", err)
			}
		})
	}
}

func BenchmarkConstraintError(b *testing.B) {
	cause := &pgconn.PgError{Code: "23505"}
	executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, cause
	}}
	query := writeDescriptor().Delete().All()

	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).Exec(b.Context()); err == nil {
			b.Fatal("missing error")
		}
	}
}

func BenchmarkConstraints(b *testing.B) {
	cause := &pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_email_key"}
	executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, cause
	}}
	table := ratel.NewTable("app", "users", []string{"id", "email", "manager_id"}, scanReadUser,
		ratel.Constraints[readUser](ratel.ConstraintError{Constraint: "users_email_key", Columns: []string{"email"}}))
	sentinel := &ratel.ConstraintError{Kind: ratel.ConstraintUnique, Table: "users"}
	query := ratel.NewWriteTable(table, func(row writeInsert) bool { return row.valid }).Delete().All()

	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		_, err := database.Run(query).Exec(b.Context())
		if !errors.Is(err, sentinel) || err.Error() == "" {
			b.Fatal("missing constraint error")
		}
	}
}
