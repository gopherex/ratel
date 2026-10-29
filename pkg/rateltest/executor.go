package rateltest

import (
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

// Executor is a scripted ratel.Executor backed by pgxmock. SQL matching is exact
// after whitespace normalization; expectations are ordered by default. The
// embedded API supplies rows, scan failures, batches, COPY and cancellation.
// Use application-owned ports, not this fake, to unit-test application use cases.
type Executor struct{ pgxmock.PgxPoolIface }

// NewExecutor creates a scripted executor and checks remaining expectations at
// cleanup. Use pgxmock.NewRows and pgxmock.NewResult to script its results.
func NewExecutor(tb testing.TB) *Executor {
	tb.Helper()

	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		tb.Fatalf("rateltest: create executor: %v", err)
	}

	tb.Cleanup(func() {
		defer mock.Close()

		if err := mock.ExpectationsWereMet(); err != nil {
			tb.Errorf("rateltest: executor expectations: %v", err)
		}
	})

	return &Executor{PgxPoolIface: mock}
}
