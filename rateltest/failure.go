package rateltest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel"
)

// Failure describes an injected PostgreSQL refusal or technical failure.
type Failure struct{ code string }

// FailWith constructs an injector for a five-character PostgreSQL SQLSTATE.
// Invalid codes panic, because they are errors in the test fixture.
func FailWith(code string) Failure {
	const sqlstateLength = 5

	if len(code) != sqlstateLength {
		panic("rateltest: SQLSTATE must contain five ASCII letters or digits")
	}

	for _, char := range code {
		if (char < '0' || char > '9') && (char < 'A' || char > 'Z') {
			panic("rateltest: SQLSTATE must contain five ASCII letters or digits")
		}
	}

	return Failure{code: code}
}

// Wrap fails matching operations without contacting the underlying executor.
// match receives SQL for queries and the sanitized table name for COPY. A batch
// containing any matching statement fails as a whole before it is sent. Each
// failure is a fresh PgError, so callers may safely enrich its metadata.
func (failure Failure) Wrap(executor ratel.Executor, match func(string) bool) ratel.Executor {
	return &failingExecutor{Executor: executor, failure: failure, match: match}
}

type failingExecutor struct {
	ratel.Executor
	failure Failure
	match   func(string) bool
}

func (executor *failingExecutor) err() *pgconn.PgError {
	return &pgconn.PgError{Severity: "ERROR", Code: executor.failure.code, Message: "rateltest: injected failure"}
}

func (executor *failingExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if executor.match(sql) {
		return pgconn.CommandTag{}, executor.err()
	}

	tag, err := executor.Executor.Exec(ctx, sql, args...)
	if err != nil {
		return tag, fmt.Errorf("rateltest: exec: %w", err)
	}

	return tag, nil
}

func (executor *failingExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if executor.match(sql) {
		return nil, executor.err()
	}

	rows, err := executor.Executor.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("rateltest: query: %w", err)
	}

	return rows, nil
}

func (executor *failingExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if executor.match(sql) {
		return failedResults{err: executor.err()}
	}

	return executor.Executor.QueryRow(ctx, sql, args...)
}

func (executor *failingExecutor) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	for _, query := range batch.QueuedQueries {
		if executor.match(query.SQL) {
			return failedResults{err: executor.err()}
		}
	}

	return executor.Executor.SendBatch(ctx, batch)
}

func (executor *failingExecutor) CopyFrom(ctx context.Context, table pgx.Identifier,
	columns []string, source pgx.CopyFromSource,
) (int64, error) {
	if executor.match(table.Sanitize()) {
		return 0, executor.err()
	}

	count, err := executor.Executor.CopyFrom(ctx, table, columns, source)
	if err != nil {
		return count, fmt.Errorf("rateltest: copy: %w", err)
	}

	return count, nil
}

type failedResults struct{ err error }

func (result failedResults) Scan(...any) error                { return result.err }
func (result failedResults) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, result.err }
func (result failedResults) Query() (pgx.Rows, error)         { return nil, result.err }
func (result failedResults) QueryRow() pgx.Row                { return result }
func (result failedResults) Close() error                     { return result.err }
