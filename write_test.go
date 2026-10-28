package ratel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gopherex/ratel"
)

type writeInsert struct {
	email string
	valid bool
	ID    ratel.Opt[int64]
	Note  ratel.Opt[ratel.Null[int64]]
}

func newWriteInsert(email string) writeInsert { return writeInsert{email: email, valid: true} }

type writeUsers struct {
	ratel.WriteTable[readUser, writeInsert]
	ID    ratel.Col[readUser, int64]
	Email ratel.Col[readUser, string]
	Note  ratel.NullCol[readUser, int64]
}

func writeDescriptor() writeUsers {
	return writeUsers{
		WriteTable: ratel.NewWriteTable(userDescriptor().Table, func(row writeInsert) bool { return row.valid },
			ratel.InsertField[writeInsert]{
				Name: "email", Type: pgx.Identifier{"pg_catalog", "text"},
				Value: func(row writeInsert) (any, bool) { return row.email, true }, Values: writeEmails,
			},
			ratel.InsertField[writeInsert]{
				Name: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
				Value: func(row writeInsert) (any, bool) { return row.ID.V, row.ID.Set }, Values: writeIDs,
			},
			ratel.InsertField[writeInsert]{
				Name: "manager_id", Type: pgx.Identifier{"pg_catalog", "int8"},
				Value: func(row writeInsert) (any, bool) { return row.Note.V, row.Note.Set }, Values: writeNotes,
			}),
		ID: ratel.NewCol[readUser, int64]("id"), Email: ratel.NewCol[readUser, string]("email"),
		Note: ratel.NewNullCol[readUser, int64]("manager_id"),
	}
}

func writeEmails(rows []writeInsert) any {
	values := make([]string, len(rows))
	for index, row := range rows {
		values[index] = row.email
	}

	return values
}

func writeIDs(rows []writeInsert) any {
	values := make([]int64, len(rows))
	for index, row := range rows {
		values[index] = row.ID.V
	}

	return values
}

func writeNotes(rows []writeInsert) any {
	values := make([]pgtype.Int8, len(rows))
	for index, row := range rows {
		values[index] = pgtype.Int8{Int64: row.Note.V.V, Valid: row.Note.V.Valid}
	}

	return values
}

func TestWriteSQL(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	database := ratel.New(readExecutor{})

	for _, tc := range []struct {
		name      string
		query     ratel.Write[readUser]
		sql       string
		args      []any
		wantError error
	}{
		{
			name: "update", query: database.Update(users).Set(users.Email.To("b")).Where(users.ID.Eq(1)),
			sql: `UPDATE "app"."users" AS "q" SET "email" = $1 WHERE "q"."id" = $2`, args: []any{"b", int64(1)},
		},
		{name: "unsafe update", query: database.Update(users).Set(users.Email.To("b")), wantError: ratel.ErrInvalidQuery},
		{name: "empty update", query: database.Update(users).All(), wantError: ratel.ErrInvalidQuery},
		{
			name: "delete", query: database.Delete(users).Where(users.ID.Eq(1)),
			sql: `DELETE FROM "app"."users" AS "q" WHERE "q"."id" = $1`, args: []any{int64(1)},
		},
		{name: "delete all", query: database.Delete(users).All(), sql: `DELETE FROM "app"."users" AS "q"`},
		{name: "unsafe delete", query: database.Delete(users), wantError: ratel.ErrInvalidQuery},
		{
			name: "duplicate assignments", query: database.Update(users).Set(users.ID.To(1), users.ID.To(2)).All(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "unknown assignment", query: database.Update(users).Set(ratel.NewCol[readUser, int64]("missing").To(1)).All(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "delete assignments", query: database.Delete(users).Set(users.ID.To(1)).All(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "null assignment", query: database.Update(users).Set(users.Note.To(ratel.Null[int64]{})).All(),
			sql: `UPDATE "app"."users" AS "q" SET "manager_id" = $1`, args: []any{ratel.Null[int64]{}},
		},
		{
			name: "conjoined where", query: database.Delete(users).Where(users.ID.Eq(1)).Where(users.Email.Eq("a")),
			sql:  `DELETE FROM "app"."users" AS "q" WHERE ("q"."id" = $1 AND "q"."email" = $2)`,
			args: []any{int64(1), "a"},
		},
		{name: "zero", wantError: ratel.ErrInvalidQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := tc.query.SQL()
			if !errors.Is(err, tc.wantError) || (err == nil && statement.SQL != tc.sql) {
				t.Fatalf("%+v: %v", statement, err)
			}

			if err == nil && !equalWriteArgs(statement.Args, tc.args) {
				t.Fatalf("args: %#v", statement.Args)
			}
		})
	}
}

func equalWriteArgs(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

type writeExecutor struct {
	ratel.Executor
	exec func(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (e writeExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.exec(ctx, sql, args...)
}

func TestWriteExecution(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"success", nil},
		{"unique", &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key", TableName: "users"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := writeExecutor{exec: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				if !equalWriteArgs(args, []any{pgx.QueryExecModeExec, "a"}) || !strings.Contains(sql, "INSERT") {
					t.Fatalf("%s %#v", sql, args)
				}

				return pgconn.NewCommandTag("INSERT 0 1"), tc.failure
			}}
			database := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeSimpleProtocol))

			count, err := database.Insert(writeDescriptor(), newWriteInsert("a")).
				Exec(t.Context(), ratel.ExecMode(pgx.QueryExecModeExec))
			if tc.failure == nil {
				if err != nil || count != 1 {
					t.Fatalf("%d %v", count, err)
				}

				return
			}

			var constraint *ratel.ConstraintError
			if !errors.As(err, &constraint) || !errors.Is(err, tc.failure) || constraint.Kind != ratel.ConstraintUnique {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkWriteSQL(b *testing.B) {
	users := writeDescriptor()
	query := ratel.New(readExecutor{}).Update(users).Set(users.Email.To("b")).Where(users.ID.Eq(1))

	b.ReportAllocs()

	for b.Loop() {
		if _, err := query.SQL(); err != nil {
			b.Fatal(err)
		}
	}
}
