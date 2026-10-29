package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel"
)

// sentStatements runs an insert on a recording executor and returns the
// statements it sent, in order.
func sentStatements(t *testing.T, query ratel.Insert[readUser, writeInsert]) ([]built, error) {
	t.Helper()

	var sent []built

	executor := writeExecutor{exec: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
		sent = append(sent, built{SQL: sql, Args: args})

		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}}
	_, err := ratel.New(executor).Run(query).Exec(t.Context())

	return sent, err
}

func TestInsertSQL(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	first := newWriteInsert("a")
	second := newWriteInsert("b")
	second.Note = ratel.OptFrom(ratel.Null[int64]{})

	for _, tc := range []struct {
		name      string
		query     ratel.Insert[readUser, writeInsert]
		sql       string
		wantError error
	}{
		{name: "insert", query: users.Insert(first), sql: `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1)`},
		{name: "invalid", query: users.Insert(writeInsert{}), wantError: ratel.ErrInvalidInsert},
		{name: "empty", query: users.Insert(), wantError: ratel.ErrInvalidInsert},
		{name: "missing conflict", query: users.Upsert(first), wantError: ratel.ErrInvalidQuery},
		{
			name: "insert action without target", query: users.Insert(first).DoNothing(),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "insert update without target", query: users.Insert(first).Update(users.Email),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "insert target without action", query: users.Insert(first).OnConflict(users.Email),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "missing action", query: users.Upsert(first).OnConflict(users.Email),
			wantError: ratel.ErrInvalidQuery,
		},
		{
			name: "duplicate target", query: users.Upsert(first).OnConflict(users.Email, users.Email).DoNothing(),
			wantError: ratel.ErrInvalidQuery,
		},
		{name: "unknown target", query: users.Upsert(first).
			OnConflict(ratel.NewCol[readUser, string]("unknown")).DoNothing(), wantError: ratel.ErrInvalidQuery},
		{
			name: "upsert", query: users.Upsert(first, newWriteInsert("b")).
				OnConflict(users.Email).Update(users.Email),
			sql: `INSERT INTO "app"."users" AS "q" ("email") SELECT * FROM unnest($1::"pg_catalog"."text"[])` +
				` ON CONFLICT ("email") DO UPDATE SET "email" = EXCLUDED."email"`,
		},
		{
			name: "do nothing", query: users.Upsert(first).OnConflict(users.Email).DoNothing(),
			sql: `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1) ON CONFLICT ("email") DO NOTHING`,
		},
		{
			name: "nullable default", query: users.Insert(second),
			sql: `INSERT INTO "app"."users" AS "q" ("email", "manager_id") VALUES ($1, $2)`,
		},
		{name: "heterogeneous", query: users.Insert(first, second), wantError: ratel.ErrInvalidQuery},
		{
			name: "invalid chunk", query: users.Insert(first).Chunk(0),
			wantError: ratel.ErrInvalidQuery,
		},
		{name: "zero", wantError: ratel.ErrInvalidInsert},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := build(tc.query)
			if !errors.Is(err, tc.wantError) || (err == nil && statement.SQL != tc.sql) {
				t.Fatalf("%+v %v", statement, err)
			}
		})
	}
}

// TestInsertGroupsBySet groups rows by their set columns through Set, the
// path generated descriptors take: Value, which boxes a value, runs only for
// the parameters of a VALUES statement.
func TestInsertGroupsBySet(t *testing.T) {
	t.Parallel()

	var boxed atomic.Int64

	users := ratel.NewWriteTable(userDescriptor().Table, func(row writeInsert) bool { return row.valid },
		ratel.InsertField[writeInsert]{
			Name: "email", Type: pgx.Identifier{"pg_catalog", "text"},
			Value: func(row writeInsert) (any, bool) {
				boxed.Add(1)

				return row.email, true
			},
			Set: func(writeInsert) bool { return true }, Values: writeEmails,
		},
		ratel.InsertField[writeInsert]{
			Name: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
			Value: func(row writeInsert) (any, bool) {
				boxed.Add(1)

				return row.ID.V, row.ID.Set
			},
			Set: func(row writeInsert) bool { return row.ID.Set }, Values: writeIDs,
		})

	withID := newWriteInsert("b")
	withID.ID = ratel.OptFrom(int64(7))

	statements, err := sentStatements(t, users.Insert(newWriteInsert("a"), newWriteInsert("c"), withID, withID))
	if err != nil || len(statements) != 2 {
		t.Fatalf("sent = %v, %v", statements, err)
	}

	for _, statement := range statements {
		if !strings.Contains(statement.SQL, "unnest(") {
			t.Fatalf("not one statement per group: %s", statement.SQL)
		}
	}

	if boxed.Load() != 0 {
		t.Fatalf("Value ran %d times for unnest statements", boxed.Load())
	}

	if _, err := sentStatements(t, users.Insert(newWriteInsert("a"))); err != nil || boxed.Load() != 1 {
		t.Fatalf("single row: %v, Value ran %d times", err, boxed.Load())
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

			query := users.Insert(tc.rows...).Chunk(tc.batch)

			statements, err := sentStatements(t, query)
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

	query := writeDescriptor().Insert(newWriteInsert("a"), writeInsert{}).Chunk(1).Returning()

	_, err := ratel.New(executor).Run(query).List(t.Context())
	if !errors.Is(err, ratel.ErrInvalidInsert) {
		t.Fatal(err)
	}
}

func TestInsertDefaultValues(t *testing.T) {
	t.Parallel()

	table := ratel.NewWriteTable(userDescriptor().Table, func(row writeInsert) bool { return row.valid })
	query := table.Insert(newWriteInsert("a"), newWriteInsert("b"))

	statements, err := sentStatements(t, query)
	if err != nil || len(statements) != 2 || !strings.HasSuffix(statements[0].SQL, "DEFAULT VALUES") {
		t.Fatalf("%+v %v", statements, err)
	}
}

func bwEmailField() ratel.InsertField[writeInsert] {
	return ratel.InsertField[writeInsert]{
		Name: "email", Type: pgx.Identifier{"pg_catalog", "text"},
		Value: func(row writeInsert) (any, bool) { return row.email, true }, Values: writeEmails,
	}
}

func bwEmailWith(mutate func(*ratel.InsertField[writeInsert])) ratel.InsertField[writeInsert] {
	field := bwEmailField()
	mutate(&field)

	return field
}

func bwArrayField() ratel.InsertField[writeInsert] {
	return ratel.InsertField[writeInsert]{
		Name: "manager_id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Value: func(writeInsert) (any, bool) { return []int64{7}, true },
	}
}

func bwTable(schema string, fields ...ratel.InsertField[writeInsert]) ratel.WriteTable[readUser, writeInsert] {
	return ratel.NewWriteTable(
		ratel.NewTable(schema, "users", []string{"id", "email", "manager_id"}, scanReadUser),
		func(row writeInsert) bool { return row.valid }, fields...)
}

func TestBwInsertRendering(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	arrays := bwTable("app", bwEmailField(), bwArrayField())
	first, second := withID("a", 1), withID("b", 2)

	for _, tc := range []struct {
		name  string
		query ratel.Insert[readUser, writeInsert]
		sql   string
		args  []any
		want  error
	}{
		{
			name:  "unnest of several columns",
			query: users.Insert(first, second),
			sql: `INSERT INTO "app"."users" AS "q" ("email", "id") SELECT * FROM unnest(` +
				`$1::"pg_catalog"."text"[], $2::"pg_catalog"."int8"[])`,
			args: []any{[]string{"a", "b"}, []int64{1, 2}},
		},
		{
			name: "base type cast to the column type",
			query: bwTable("app", bwEmailWith(func(f *ratel.InsertField[writeInsert]) {
				f.Type, f.Base = pgx.Identifier{"app", "email"}, pgx.Identifier{"pg_catalog", "text"}
			})).Insert(newWriteInsert("a"), newWriteInsert("b")),
			sql: `INSERT INTO "app"."users" AS "q" ("email") SELECT * FROM unnest(` +
				`$1::"pg_catalog"."text"[]::"app"."email"[])`,
			args: []any{[]string{"a", "b"}},
		},
		{
			name: "invalid base identifier",
			query: bwTable("app", bwEmailWith(func(f *ratel.InsertField[writeInsert]) {
				f.Base = pgx.Identifier{"pg_catalog", ""}
			})).Insert(newWriteInsert("a")),
			want: ratel.ErrInvalidQuery,
		},
		{
			name:  "array column falls back to VALUES",
			query: arrays.Insert(newWriteInsert("a"), newWriteInsert("b")),
			sql:   `INSERT INTO "app"."users" AS "q" ("email", "manager_id") VALUES ($1, $2), ($3, $4)`,
			args:  []any{"a", []int64{7}, "b", []int64{7}},
		},
		{
			name: "composite conflict target and update",
			query: users.Upsert(newWriteInsert("a")).
				OnConflict(users.Email, users.ID).Update(users.Email, users.ID),
			sql: `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1) ON CONFLICT ("email", "id") ` +
				`DO UPDATE SET "email" = EXCLUDED."email", "id" = EXCLUDED."id"`,
			args: []any{"a"},
		},
		{
			name:  "update after do nothing replaces it",
			query: users.Upsert(newWriteInsert("a")).OnConflict(users.Email).DoNothing().Update(users.ID),
			sql: `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1) ON CONFLICT ("email") ` +
				`DO UPDATE SET "id" = EXCLUDED."id"`,
			args: []any{"a"},
		},
		{
			name:  "schema is required",
			query: bwTable("", bwEmailField()).Insert(newWriteInsert("a")),
			want:  ratel.ErrInvalidQuery,
		},
		{
			name: "invalid type identifier",
			query: bwTable("app", bwEmailWith(func(f *ratel.InsertField[writeInsert]) {
				f.Type = pgx.Identifier{""}
			})).Insert(newWriteInsert("a")),
			want: ratel.ErrInvalidQuery,
		},
		{
			name: "field outside the table columns",
			query: bwTable("app", bwEmailWith(func(f *ratel.InsertField[writeInsert]) {
				f.Name = "unknown"
			})).Insert(newWriteInsert("a")),
			want: ratel.ErrInvalidQuery,
		},
		{
			name:  "duplicate field",
			query: bwTable("app", bwEmailField(), bwEmailField()).Insert(newWriteInsert("a")),
			want:  ratel.ErrInvalidQuery,
		},
		{
			name: "field without Values uses VALUES",
			query: bwTable("app", bwEmailWith(func(f *ratel.InsertField[writeInsert]) {
				f.Values = nil
			})).Insert(newWriteInsert("a"), newWriteInsert("b")),
			sql:  `INSERT INTO "app"."users" AS "q" ("email") VALUES ($1), ($2)`,
			args: []any{"a", "b"},
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

// TestBwInsertParameterLimit checks that array-column inserts, which cannot use
// unnest, stay under the 65535 parameters of one extended-protocol statement.
func TestBwInsertParameterLimit(t *testing.T) {
	t.Parallel()

	const perStatement = 65535 / 2

	arrays := bwTable("app", bwEmailField(), bwArrayField())
	rows := slices.Repeat([]writeInsert{newWriteInsert("a")}, perStatement+1)

	statements, err := sentStatements(t, arrays.Insert(rows...).Chunk(1_000_000))
	if err != nil || len(statements) != 2 {
		t.Fatalf("statements = %d, %v", len(statements), err)
	}

	if len(statements[0].Args) != perStatement*2 || len(statements[1].Args) != 2 {
		t.Fatalf("args = %d and %d", len(statements[0].Args), len(statements[1].Args))
	}
}

func TestBwInsertExecInvalid(t *testing.T) {
	t.Parallel()

	executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		t.Fatal("exec reached the executor")

		return pgconn.CommandTag{}, nil
	}}

	count, err := ratel.New(executor).Run(writeDescriptor().Insert()).Exec(t.Context())
	if count != 0 || !errors.Is(err, ratel.ErrInvalidInsert) {
		t.Fatalf("Exec() = %d, %v", count, err)
	}
}

func BenchmarkBwInsertArrays(b *testing.B) {
	arrays := bwTable("app", bwEmailField(), bwArrayField())
	query := arrays.Insert(slices.Repeat([]writeInsert{newWriteInsert("a")}, 100)...)
	database := ratel.New(benchWriteExecutor())

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).Exec(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInsertTerminals(b *testing.B) {
	users := writeDescriptor()
	database := ratel.New(benchWriteExecutor())
	rows := []writeInsert{newWriteInsert("a")}

	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"exec", func(ctx context.Context) error {
			_, err := database.Run(users.Insert(rows...)).Exec(ctx)

			return err
		}},
		{"returning", func(ctx context.Context) error {
			_, err := database.Run(users.Insert(rows...).Returning()).List(ctx)

			return err
		}},
		{"do nothing", func(ctx context.Context) error {
			_, err := database.Run(users.Upsert(rows...).OnConflict(users.Email).DoNothing()).Exec(ctx)

			return err
		}},
		{"batch rows", func(ctx context.Context) error {
			_, err := database.Run(users.Insert(newWriteInsert("a"), newWriteInsert("b")).Chunk(1)).Exec(ctx)

			return err
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if err := tc.run(b.Context()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// benchWriteExec serves one inserted row for both Exec and RETURNING.
type benchWriteExec struct{ ratel.Executor }

func (benchWriteExec) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &bwRows{readRows: readRows{ids: []int64{1}}, tag: "INSERT 0 1"}, nil
}

func (benchWriteExec) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func benchWriteExecutor() benchWriteExec { return benchWriteExec{} }

func BenchmarkInsertSQL(b *testing.B) {
	users := writeDescriptor()
	query := users.Upsert(newWriteInsert("a"), newWriteInsert("b")).
		OnConflict(users.Email).Update(users.Email)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := build(query); err != nil {
			b.Fatal(err)
		}
	}
}
