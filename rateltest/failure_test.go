package rateltest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v5"

	"github.com/gopherex/ratel"

	"github.com/gopherex/ratel/rateltest"
)

func TestFailWith(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"23505", "55P03", "57014"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			base := rateltest.NewExecutor(t)
			executor := rateltest.FailWith(code).Wrap(base, func(sql string) bool { return sql == "fail" || sql == `"fail"` })
			check := func(err error) {
				t.Helper()

				var failure *pgconn.PgError
				if !errors.As(err, &failure) || failure.Code != code {
					t.Fatalf("error = %v", err)
				}
			}
			_, err := executor.Exec(t.Context(), "fail")
			check(err)
			rows, err := executor.Query(t.Context(), "fail")
			check(err)

			if rows != nil {
				rows.Close()
				t.Fatal("failed query returned rows")
			}

			check(executor.QueryRow(t.Context(), "fail").Scan())

			batch := &pgx.Batch{}
			batch.Queue("fail")
			results := executor.SendBatch(t.Context(), batch)
			_, err = results.Exec()
			check(err)
			rows, err = results.Query()
			check(err)

			if rows != nil {
				rows.Close()
				t.Fatal("failed batch returned rows")
			}

			check(results.QueryRow().Scan())
			check(results.Close())

			_, err = executor.CopyFrom(t.Context(), pgx.Identifier{"fail"}, []string{"value"}, pgx.CopyFromRows(nil))
			check(err)
			base.ExpectExec("pass").WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			if _, err = executor.Exec(t.Context(), "pass"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFailWithPassThrough(t *testing.T) {
	t.Parallel()
	base := rateltest.NewExecutor(t)
	executor := rateltest.FailWith("23505").Wrap(base, func(string) bool { return false })
	base.ExpectQuery("query").WillReturnRows(pgxmock.NewRows([]string{"v"}).AddRow(1))

	rows, err := executor.Query(t.Context(), "query")
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	base.ExpectQuery("row").WillReturnRows(pgxmock.NewRows([]string{"v"}).AddRow(1))

	var value int
	if err = executor.QueryRow(t.Context(), "row").Scan(&value); err != nil {
		t.Fatal(err)
	}

	base.ExpectBatch().ExpectExec("batch").WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	batch := &pgx.Batch{}
	batch.Queue("batch")

	results := executor.SendBatch(t.Context(), batch)
	if _, err = results.Exec(); err != nil {
		t.Fatal(err)
	}

	if err = results.Close(); err != nil {
		t.Fatal(err)
	}

	base.ExpectCopyFrom(pgx.Identifier{"app", "values"}, []string{"v"}).WillReturnResult(1)

	if _, err = executor.CopyFrom(t.Context(), pgx.Identifier{"app", "values"}, []string{"v"},
		pgx.CopyFromRows([][]any{{1}})); err != nil {
		t.Fatal(err)
	}
}

func TestFailWithDelegateErrors(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"exec", "query", "copy"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()

			base := rateltest.NewExecutor(t)
			executor := rateltest.FailWith("23505").Wrap(base, func(string) bool { return false })

			var err error

			switch operation {
			case "exec":
				base.ExpectExec("pass").WillReturnError(context.Canceled)

				_, err = executor.Exec(t.Context(), "pass")
			case "query":
				base.ExpectQuery("pass").WillReturnError(context.Canceled)

				rows, queryErr := executor.Query(t.Context(), "pass")
				if rows != nil {
					rows.Close()
					t.Fatal("unexpected rows")
				}

				err = queryErr
			case "copy":
				base.ExpectCopyFrom(pgx.Identifier{"app", "items"}, []string{"id"}).WillReturnError(context.Canceled)

				_, err = executor.CopyFrom(t.Context(), pgx.Identifier{"app", "items"}, []string{"id"}, pgx.CopyFromRows(nil))
			}

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("lost delegated cause: %v", err)
			}
		})
	}
}

func TestFailWithRefusal(t *testing.T) {
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
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()

			executor := rateltest.FailWith(tc.code).Wrap(rateltest.NewExecutor(t), func(string) bool { return true })
			table := ratel.NewTable("app", "items", []string{"id"}, func(row pgx.Row) (int64, error) {
				var value int64
				err := row.Scan(&value)

				return value, err
			})
			writable := ratel.NewWriteTable(table, func(int64) bool { return true })
			_, err := ratel.New(executor).Delete(writable).All().Exec(t.Context())

			var refusal *ratel.ConstraintError
			if !errors.As(err, &refusal) || refusal.Kind != tc.kind {
				t.Fatalf("refusal = %v", err)
			}
		})
	}
}

func TestFailWithInvalidCode(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"", "1234", "123456", "abcde", "23é1"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatal("invalid SQLSTATE accepted")
				}
			}()

			_ = rateltest.FailWith(code)
		})
	}
}

func BenchmarkFailure(b *testing.B) {
	executor := rateltest.FailWith("23505").Wrap(rateltest.NewExecutor(b), func(string) bool { return true })
	for b.Loop() {
		_, _ = executor.Exec(b.Context(), "fail")
	}
}
