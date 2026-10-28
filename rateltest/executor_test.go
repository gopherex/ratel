package rateltest_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"

	"github.com/gopherex/ratel"

	"github.com/gopherex/ratel/rateltest"
)

func TestExecutorRuntime(t *testing.T) {
	t.Parallel()

	executor := rateltest.NewExecutor(t)
	executor.ExpectQuery(`SELECT "q"."id" FROM "app"."items" AS "q"`).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(7))).RowsWillBeClosed()

	table := ratel.NewTable("app", "items", []string{"id"}, func(row pgx.Row) (int64, error) {
		var value int64
		err := row.Scan(&value)

		return value, err
	})

	values, err := ratel.New(executor).From(table).List(t.Context())
	if err != nil || len(values) != 1 || values[0] != 7 {
		t.Fatalf("runtime list = %v, %v", values, err)
	}
}

func TestExecutor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value int64
	}{{"positive", 7}, {"zero", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := rateltest.NewExecutor(t)

			var _ ratel.Executor = executor

			executor.ExpectQuery("SELECT value").WithArgs(tc.value).
				WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow(tc.value))

			var got int64
			if err := executor.QueryRow(t.Context(), "SELECT value", tc.value).Scan(&got); err != nil || got != tc.value {
				t.Fatalf("scan = %d, %v", got, err)
			}
		})
	}
}
