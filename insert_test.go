package ratel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestInsertSQL(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	database := ratel.New(readExecutor{})
	first := newWriteInsert("a")
	second := newWriteInsert("b")
	second.Note = ratel.OptFrom(ratel.Null[int64]{})

	for _, tc := range []struct {
		name      string
		query     ratel.Insert[readUser, writeInsert]
		sql       string
		wantError error
	}{
		{name: "insert", query: database.Insert(users, first), sql: `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1)`},
		{name: "invalid", query: database.Insert(users, writeInsert{}), wantError: ratel.ErrInvalidInsert},
		{name: "empty", query: database.Insert(users), wantError: ratel.ErrInvalidInsert},
		{name: "missing conflict", query: database.Upsert(users, first), wantError: ratel.ErrInvalidQuery},
		{
			name: "insert action without target", query: database.Insert(users, first).DoNothing(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "insert update without target", query: database.Insert(users, first).Update(users.Email),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "insert target without action", query: database.Insert(users, first).OnConflict(users.Email),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "missing action", query: database.Upsert(users, first).OnConflict(users.Email),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "duplicate target", query: database.Upsert(users, first).OnConflict(users.Email, users.Email).DoNothing(),
			wantError: ratel.ErrInvalidQuery,
		},
		{name: "unknown target", query: database.Upsert(users, first).
			OnConflict(ratel.NewCol[readUser, string]("unknown")).DoNothing(), wantError: ratel.ErrInvalidQuery},
		{
			name: "upsert", query: database.Upsert(users, first, newWriteInsert("b")).
				OnConflict(users.Email).Update(users.Email),
			sql: `INSERT INTO "app"."users" AS "q" ("email") SELECT * FROM unnest($1::"pg_catalog"."text"[])` +
				` ON CONFLICT ("email") DO UPDATE SET "email" = EXCLUDED."email"`,
		},
		{
			name: "do nothing", query: database.Upsert(users, first).OnConflict(users.Email).DoNothing(),
			sql: `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1) ON CONFLICT ("email") DO NOTHING`,
		},
		{
			name: "nullable default", query: database.Insert(users, second),
			sql: `INSERT INTO "app"."users" AS "q" ("email", "manager_id") VALUES ($1, $2)`,
		},
		{name: "heterogeneous", query: database.Insert(users, first, second), wantError: ratel.ErrInvalidQuery},
		{
			name: "invalid batch", query: ratel.New(readExecutor{}, ratel.WithBatchRows(0)).Insert(users, first),
			wantError: ratel.ErrInvalidQuery,
		},
		{name: "zero", wantError: ratel.ErrInvalidInsert},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := tc.query.SQL()
			if !errors.Is(err, tc.wantError) || (err == nil && statement.SQL != tc.sql) {
				t.Fatalf("%+v %v", statement, err)
			}
		})
	}
}

func TestInsertBatches(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	first := newWriteInsert("a")
	second := newWriteInsert("b")
	second.ID = ratel.OptFrom(int64(42))

	for _, tc := range []struct {
		name  string
		rows  []writeInsert
		batch int
		count int
	}{
		{"chunks", []writeInsert{first, first, first}, 2, 2},
		{"shapes", []writeInsert{first, second, first}, 100, 3},
		{"same shape", []writeInsert{first, first}, 100, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			query := ratel.New(readExecutor{}, ratel.WithBatchRows(tc.batch)).Insert(users, tc.rows...)

			statements, err := query.Statements()
			if err != nil || len(statements) != tc.count {
				t.Fatalf("%+v %v", statements, err)
			}

			if len(tc.rows) > 1 && tc.count == 1 {
				switch values := statements[0].Args[0].(type) {
				case []string:
					if len(values) != len(tc.rows) || values[0] != "a" {
						t.Fatalf("%#v", values)
					}
				default:
					t.Fatalf("not a parallel array: %T", statements[0].Args[0])
				}
			}
		})
	}
}

func TestInvalidInsertNeverExecutes(t *testing.T) {
	t.Parallel()

	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		t.Fatal("SQL sent")

		return nil, ratel.ErrInvalidQuery
	}}

	_, err := ratel.New(executor, ratel.WithBatchRows(1)).Insert(writeDescriptor(), newWriteInsert("a"), writeInsert{}).
		Returning(t.Context())
	if !errors.Is(err, ratel.ErrInvalidInsert) {
		t.Fatal(err)
	}
}

func TestInsertDefaultValues(t *testing.T) {
	t.Parallel()

	table := ratel.NewWriteTable(userDescriptor().Table, func(row writeInsert) bool { return row.valid })
	query := ratel.New(readExecutor{}).Insert(table, newWriteInsert("a"), newWriteInsert("b"))

	statements, err := query.Statements()
	if err != nil || len(statements) != 2 || !strings.HasSuffix(statements[0].SQL, "DEFAULT VALUES") {
		t.Fatalf("%+v %v", statements, err)
	}
}

func BenchmarkInsertSQL(b *testing.B) {
	users := writeDescriptor()
	query := ratel.New(readExecutor{}).Upsert(users, newWriteInsert("a"), newWriteInsert("b")).
		OnConflict(users.Email).Update(users.Email)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := query.SQL(); err != nil {
			b.Fatal(err)
		}
	}
}
