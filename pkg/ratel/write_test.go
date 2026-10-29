package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gopherex/ratel/pkg/ratel"
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

	for _, tc := range []struct {
		name      string
		query     ratel.Write[readUser]
		sql       string
		args      []any
		wantError error
	}{
		{
			name: "update", query: users.Update().Set(users.Email.To("b")).Where(users.ID.Eq(1)),
			sql: `UPDATE "app"."users" AS "q" SET "email" = $1 WHERE "q"."id" = $2`, args: []any{"b", int64(1)},
		},
		{name: "unsafe update", query: users.Update().Set(users.Email.To("b")), wantError: ratel.ErrInvalidQuery},
		{name: "empty update", query: users.Update().All(), wantError: ratel.ErrInvalidQuery},
		{
			name: "delete", query: users.Delete().Where(users.ID.Eq(1)),
			sql: `DELETE FROM "app"."users" AS "q" WHERE "q"."id" = $1`, args: []any{int64(1)},
		},
		{name: "delete all", query: users.Delete().All(), sql: `DELETE FROM "app"."users" AS "q"`},
		{name: "unsafe delete", query: users.Delete(), wantError: ratel.ErrInvalidQuery},
		{
			name: "duplicate assignments", query: users.Update().Set(users.ID.To(1), users.ID.To(2)).All(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "unknown assignment", query: users.Update().Set(ratel.NewCol[readUser, int64]("missing").To(1)).All(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "delete assignments", query: users.Delete().Set(users.ID.To(1)).All(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "null assignment", query: users.Update().Set(users.Note.To(ratel.Null[int64]{})).All(),
			sql: `UPDATE "app"."users" AS "q" SET "manager_id" = $1`, args: []any{nil},
		},
		{
			name: "conjoined where", query: users.Delete().Where(users.ID.Eq(1)).Where(users.Email.Eq("a")),
			sql:  `DELETE FROM "app"."users" AS "q" WHERE ("q"."id" = $1 AND "q"."email" = $2)`,
			args: []any{int64(1), "a"},
		},
		{name: "zero", wantError: ratel.ErrInvalidQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := build(tc.query)
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

			insert := writeDescriptor().Insert(newWriteInsert("a"))

			count, err := database.Run(insert).Exec(t.Context(), ratel.ExecMode(pgx.QueryExecModeExec))
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

// bwRows is readRows that also reports a command tag, as RETURNING reads need.
type bwRows struct {
	readRows
	tag string
}

func (r *bwRows) CommandTag() pgconn.CommandTag { return pgconn.NewCommandTag(r.tag) }

func TestBwWriteSQL(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	noSchema := ratel.NewWriteTable(ratel.NewTable("", "users", []string{"id", "email"}, scanReadUser),
		func(row writeInsert) bool { return row.valid })

	for _, tc := range []struct {
		name  string
		query ratel.Write[readUser]
		sql   string
		args  []any
		want  error
	}{
		{
			name:  "several assignments",
			query: users.Update().Set(users.Email.To("b"), users.ID.To(9)).Where(users.ID.Eq(1)),
			sql:   `UPDATE "app"."users" AS "q" SET "email" = $1, "id" = $2 WHERE "q"."id" = $3`,
			args:  []any{"b", int64(9), int64(1)},
		},
		{
			name: "set in several calls", query: users.Update().Set(users.Email.To("b")).Set(users.ID.To(9)).All(),
			sql:  `UPDATE "app"."users" AS "q" SET "email" = $1, "id" = $2`,
			args: []any{"b", int64(9)},
		},
		{name: "update without schema", query: noSchema.Update().All(), want: ratel.ErrInvalidQuery},
		{name: "delete without schema", query: noSchema.Delete().All(), want: ratel.ErrInvalidQuery},
		{
			name:  "invalid condition",
			query: users.Delete().Where(ratel.Col[readUser, int64]{}.Eq(1)),
			want:  ratel.ErrInvalidQuery,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := build(tc.query)
			if !errors.Is(err, tc.want) || statement.SQL != tc.sql || !reflect.DeepEqual(statement.Args, tc.args) {
				t.Fatalf("SQL() = %+v, %v; want %q, %#v, %v", statement, err, tc.sql, tc.args, tc.want)
			}
		})
	}
}

func TestBwWriteInvalidNeverExecutes(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		t.Fatal("exec reached the executor")

		return pgconn.CommandTag{}, nil
	}}
	database := ratel.New(executor)
	unsafe := users.Delete()

	count, err := database.Run(unsafe).Exec(t.Context())
	if count != 0 || !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Exec() = %d, %v", count, err)
	}

	models, err := database.Run(unsafe.Returning()).List(t.Context())
	if models != nil || !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Returning() = %v, %v", models, err)
	}
}

func TestBwWriteReturning(t *testing.T) {
	t.Parallel()

	const returning = ` RETURNING "q"."id", "q"."email", "q"."manager_id"`

	offline := errors.New("offline")
	scanFailure := errors.New("scan failed")
	iterFailure := errors.New("iteration failed")
	unique := &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}
	users := keyedDescriptor(true)

	for _, tc := range []struct {
		name    string
		write   func(db *ratel.DB) ratel.Write[readUser]
		rows    *bwRows
		queryEr error
		sql     string
		want    []int64
		wantErr error
	}{
		{
			name:  "updated rows",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.Update().Set(users.Email.To("b")).All() },
			rows:  &bwRows{readRows: readRows{ids: []int64{1, 2}}, tag: "UPDATE 2"},
			sql:   `UPDATE "app"."users" AS "q" SET "email" = $1` + returning,
			want:  []int64{1, 2},
		},
		{
			name:  "no rows is an empty result",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.Delete().All() },
			rows:  &bwRows{tag: "DELETE 0"},
			sql:   `DELETE FROM "app"."users" AS "q"` + returning,
			want:  []int64{},
		},
		{
			name:  "row write without a match",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.DeleteRow(readUser{ID: 7}) },
			rows:  &bwRows{tag: "DELETE 0"},
			sql:   `DELETE FROM "app"."users" AS "q" WHERE "q"."id" = $1` + returning, wantErr: ratel.ErrNotFound,
		},
		{
			name:  "row write with a match",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.DeleteRow(readUser{ID: 7}) },
			rows:  &bwRows{readRows: readRows{ids: []int64{7}}, tag: "DELETE 1"},
			sql:   `DELETE FROM "app"."users" AS "q" WHERE "q"."id" = $1` + returning, want: []int64{7},
		},
		{
			name:  "query failure",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.Delete().All() },
			sql:   `DELETE FROM "app"."users" AS "q"` + returning, queryEr: offline, wantErr: offline,
		},
		{
			name:  "constraint refusal",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.Update().Set(users.Email.To("b")).All() },
			sql:   `UPDATE "app"."users" AS "q" SET "email" = $1` + returning, queryEr: unique,
			wantErr: &ratel.ConstraintError{Kind: ratel.ConstraintUnique, Constraint: "users_email_key"},
		},
		{
			name:  "scan failure",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.Delete().All() },
			rows:  &bwRows{readRows: readRows{ids: []int64{1}, scanFailure: scanFailure}, tag: "DELETE 1"},
			sql:   `DELETE FROM "app"."users" AS "q"` + returning, wantErr: scanFailure,
		},
		{
			name:  "iteration failure",
			write: func(_ *ratel.DB) ratel.Write[readUser] { return users.Delete().All() },
			rows:  &bwRows{readRows: readRows{failure: iterFailure}, tag: "DELETE 0"},
			sql:   `DELETE FROM "app"."users" AS "q"` + returning, wantErr: iterFailure,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := readExecutor{query: func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
				if sql != tc.sql {
					t.Fatalf("sql = %q, want %q", sql, tc.sql)
				}

				if tc.queryEr != nil {
					return nil, tc.queryEr
				}

				return tc.rows, nil
			}}

			models, err := ratel.New(executor).Run(tc.write(nil).Returning()).List(t.Context())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Returning() error = %v, want %v", err, tc.wantErr)
			}

			if tc.wantErr != nil {
				if models != nil {
					t.Fatalf("partial result on failure: %v", models)
				}

				return
			}

			ids := make([]int64, len(models))
			for index, model := range models {
				ids[index] = model.ID
			}

			if !reflect.DeepEqual(ids, tc.want) || models == nil {
				t.Fatalf("ids = %v, want %v", ids, tc.want)
			}
		})
	}
}

func BenchmarkBwWriteReturning(b *testing.B) {
	users := writeDescriptor()
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &bwRows{readRows: readRows{ids: []int64{1, 2, 3}}, tag: "UPDATE 3"}, nil
	}}
	query := users.Update().Set(users.Email.To("b")).All().Returning()
	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteSQL(b *testing.B) {
	users := writeDescriptor()
	query := users.Update().Set(users.Email.To("b")).Where(users.ID.Eq(1))

	b.ReportAllocs()

	for b.Loop() {
		if _, err := build(query); err != nil {
			b.Fatal(err)
		}
	}
}
