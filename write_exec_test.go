package ratel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel"
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
				func() ([]readUser, error) { return database.Insert(users, newWriteInsert("a")).Returning(t.Context()) },
				func() ([]readUser, error) {
					return database.Update(users).Set(users.Email.To("a")).All().Returning(t.Context())
				},
				func() ([]readUser, error) { return database.Delete(users).All().Returning(t.Context()) },
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
			_, err := database.Update(users).Set(users.Email.To("a")).All().Exec(t.Context())

			return err
		},
		"delete returning": func() error {
			_, err := database.Delete(users).All().Returning(t.Context())

			return err
		},
		"insert exec": func() error {
			_, err := database.Insert(users, newWriteInsert("a")).Exec(t.Context())

			return err
		},
		"insert returning": func() error {
			_, err := database.Insert(users, newWriteInsert("a")).Returning(t.Context())

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

func BenchmarkWriteExec(b *testing.B) {
	executor := writeExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 1"), nil
	}}
	query := ratel.New(executor).Delete(writeDescriptor()).All()

	b.ReportAllocs()

	for b.Loop() {
		if _, err := query.Exec(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
