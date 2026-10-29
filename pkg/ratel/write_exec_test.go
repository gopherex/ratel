package ratel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel/pkg/ratel"
)

type returningRows struct{ *readRows }

func (r returningRows) CommandTag() pgconn.CommandTag { return pgconn.NewCommandTag("INSERT 0 1") }

func TestWriteReturning(t *testing.T) {
	t.Parallel()

	failure := errors.New("offline")
	for _, tc := range []struct {
		name       string
		ids        []int64
		queryError error
		scanError  error
		rowError   error
	}{
		{name: "success", ids: []int64{42}},
		{name: "empty"},
		{name: "query error", queryError: failure},
		{name: "scan error", ids: []int64{42}, scanError: failure},
		{name: "iteration error", ids: []int64{42}, rowError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows := &readRows{ids: tc.ids, scanFailure: tc.scanError, failure: tc.rowError}
			executor := readExecutor{query: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
				if !strings.Contains(sql, ` RETURNING "q"."id", "q"."email", "q"."manager_id"`) {
					t.Fatal(sql)
				}
				if len(args) == 0 || args[0] != pgx.QueryExecModeExec {
					t.Fatalf("mode missing: %#v", args)
				}

				return returningRows{rows}, tc.queryError
			}}
			users := writeDescriptor()
			database := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeExec))

			queries := []func() ([]readUser, error){
				func() ([]readUser, error) {
					return database.Run(users.Insert(newWriteInsert("a")).Returning()).List(t.Context())
				},
				func() ([]readUser, error) {
					return database.Run(users.Update().Set(users.Email.To("a")).All().Returning()).List(t.Context())
				},
				func() ([]readUser, error) { return database.Run(users.Delete().All().Returning()).List(t.Context()) },
			}
			for _, query := range queries {
				rows.index = 0
				rows.closed = false

				models, err := query()
				wantError := errors.Join(tc.queryError, tc.scanError, tc.rowError)
				checkWriteResult(t, models, err, len(tc.ids), wantError)

				if tc.queryError == nil && !rows.closed {
					t.Fatal("cursor not closed")
				}
			}
		})
	}
}

func checkWriteResult(t *testing.T, models []readUser, err error, count int, wantError error) {
	t.Helper()

	if wantError != nil {
		if err == nil || !errors.Is(wantError, errors.Unwrap(err)) || models != nil {
			t.Fatalf("partial results: %+v %v", models, err)
		}

		return
	}

	if err != nil || len(models) != count || models == nil {
		t.Fatalf("%+v %v", models, err)
	}
}

func TestBwWriteWithoutDB(t *testing.T) {
	t.Parallel()

	var database *ratel.DB

	users := writeDescriptor()

	for name, run := range map[string]func() error{
		"update exec": func() error {
			_, err := database.Run(users.Update().Set(users.Email.To("a")).All()).Exec(t.Context())

			return err
		},
		"delete returning": func() error {
			_, err := database.Run(users.Delete().All().Returning()).List(t.Context())

			return err
		},
		"insert exec": func() error {
			_, err := database.Run(users.Insert(newWriteInsert("a"))).Exec(t.Context())

			return err
		},
		"insert returning": func() error {
			_, err := database.Run(users.Insert(newWriteInsert("a")).Returning()).List(t.Context())

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

func TestReturningBuild(t *testing.T) {
	t.Parallel()

	const returning = ` RETURNING "q"."id", "q"."email", "q"."manager_id"`

	users := writeDescriptor()

	for name, tc := range map[string]struct {
		query buildable
		want  string
	}{
		"insert": {
			users.Insert(newWriteInsert("a")).Returning(),
			`INSERT INTO "app"."users" AS "q" ("email") VALUES ($1)` + returning,
		},
		"update": {
			users.Update().Set(users.Email.To("b")).All().Returning(),
			`UPDATE "app"."users" AS "q" SET "email" = $1` + returning,
		},
		"delete": {
			users.Delete().Where(users.ID.Eq(1)).Returning(),
			`DELETE FROM "app"."users" AS "q" WHERE "q"."id" = $1` + returning,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got, err := build(tc.query); err != nil || got.SQL != tc.want {
				t.Fatalf("Build() = %+v, %v; want %s", got, err, tc.want)
			}
		})
	}

	for name, query := range map[string]buildable{
		"several insert statements": users.Insert(newWriteInsert("a"), newWriteInsert("b")).Chunk(1).Returning(),
		"unsafe delete":             users.Delete().Returning(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := build(query); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("Build() error = %v", err)
			}
		})
	}
}

func TestRowRunnerVerbs(t *testing.T) {
	t.Parallel()

	first, second := newWriteInsert("a"), newWriteInsert("b")

	for _, tc := range []struct {
		name  string
		query ratel.InsertReturning[readUser, writeInsert]
		ids   []int64
		sent  int
		list  int
		one   error
		first int64
	}{
		{"one chunk", writeDescriptor().Insert(first).Returning(), []int64{7}, 1, 1, nil, 7},
		{"two rows", writeDescriptor().Insert(first, second).Returning(), []int64{7, 8}, 1, 2, ratel.ErrMultipleRows, 7},
		{
			"every chunk runs", writeDescriptor().Insert(first, second).Chunk(1).Returning(),
			[]int64{7},
			2, 2, ratel.ErrMultipleRows, 7,
		},
		{"no rows", writeDescriptor().Insert(first).Returning(), nil, 1, 0, ratel.ErrNotFound, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sent := 0
			database := ratel.New(readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
				sent++

				return &readRows{ids: tc.ids}, nil
			}})

			models, err := database.Run(tc.query).List(t.Context())
			if err != nil || len(models) != tc.list || sent != tc.sent {
				t.Fatalf("List() = %v, %v, sent %d", models, err, sent)
			}

			sent = 0

			if _, err = database.Run(tc.query).One(t.Context()); !errors.Is(err, tc.one) || sent != tc.sent {
				t.Fatalf("One() = %v, sent %d", err, sent)
			}

			model, err := database.Run(tc.query).First(t.Context())
			if tc.first == 0 && !errors.Is(err, ratel.ErrNotFound) || tc.first != 0 && (err != nil || model.ID != tc.first) {
				t.Fatalf("First() = %v, %v", model, err)
			}

			count := 0

			for _, err = range database.Run(tc.query).All(t.Context()) {
				if err != nil {
					t.Fatal(err)
				}

				count++
			}

			if count != tc.list {
				t.Fatalf("All() yielded %d, want %d", count, tc.list)
			}
		})
	}
}

func TestRowRunnerRefusals(t *testing.T) {
	t.Parallel()

	users := keyedDescriptor(true)
	user := readUser{ID: 7, Email: "a@x"}
	database := ratel.New(readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &readRows{}, nil
	}})

	for _, tc := range []struct {
		name  string
		query ratel.WriteReturning[readUser]
		want  error
	}{
		{"delete missing", users.DeleteRow(user).Returning(), ratel.ErrNotFound},
		{"stale version", users.Save(user).ExpectVersion(users.Email, "old").Returning(), ratel.ErrStaleVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := database.Run(tc.query).List(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("List() = %v", err)
			}

			if _, err := database.Run(tc.query).One(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("One() = %v", err)
			}

			for _, err := range database.Run(tc.query).All(t.Context()) {
				if !errors.Is(err, tc.want) {
					t.Fatalf("All() = %v", err)
				}
			}
		})
	}
}

func TestExecRunnerChunks(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	sent := 0
	database := ratel.New(writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		sent++

		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}})
	query := users.Insert(newWriteInsert("a"), newWriteInsert("b"), newWriteInsert("c")).Chunk(2)

	if _, _, err := query.Build(); !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Build() of two statements = %v", err)
	}

	count, err := database.Run(query).Exec(t.Context())
	if err != nil || count != 2 || sent != 2 {
		t.Fatalf("Exec() = %d, %v, sent %d", count, err, sent)
	}
}

func BenchmarkRowRunner(b *testing.B) {
	database := ratel.New(readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &readRows{ids: []int64{1, 2, 3}}, nil
	}})
	query := writeDescriptor().Delete().All().Returning()

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteExec(b *testing.B) {
	executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 1"), nil
	}}
	query := writeDescriptor().Delete().All()

	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).Exec(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
