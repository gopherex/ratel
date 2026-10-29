package rateltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Beginner is satisfied by pgx pools, connections and transactions.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RollbackTx begins a transaction and rolls it back at cleanup, including after
// cancellation or an aborted statement. Use it only when the code under test does
// not manage its own transactions. A committed transaction cannot be isolated.
// The optional timeout bounds begin and cleanup separately; default: five minutes.
func RollbackTx(tb testing.TB, pool Beginner, timeout ...time.Duration) pgx.Tx {
	tb.Helper()

	const defaultTimeout = 5 * time.Minute

	duration := defaultTimeout

	if len(timeout) > 1 {
		tb.Fatal("rateltest: RollbackTx accepts at most one timeout")
	}

	if len(timeout) == 1 {
		duration = timeout[0]
	}

	if duration <= 0 {
		tb.Fatal("rateltest: rollback timeout must be positive")
	}

	ctx, cancel := context.WithTimeout(tb.Context(), duration)
	defer cancel()

	transaction, err := pool.Begin(ctx)
	if err != nil {
		tb.Fatalf("rateltest: begin transaction: %v", err)
	}

	tb.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(tb.Context()), duration)
		defer cleanupCancel()

		if err := transaction.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			tb.Errorf("rateltest: rollback transaction: %v", err)
		}
	})

	return transaction
}
