package rateltest_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/pkg/rateltest"
)

func TestRollbackTx(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
	}{{name: "open"}, {name: "already closed", err: pgx.ErrTxClosed}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := rateltest.NewExecutor(t)
			executor.ExpectBegin()
			executor.ExpectRollback().WillReturnError(tc.err)

			transaction := rateltest.RollbackTx(t, executor)
			if transaction == nil {
				t.Fatal("missing transaction")
			}
		})
	}
}
