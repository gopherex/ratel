// Package ratel provides PostgreSQL data access over a caller-owned executor.
package ratel

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Executor provides the native pgx operations used by ratel.
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, source pgx.CopyFromSource) (int64, error)
}

// DB executes operations on a caller-owned executor.
type DB struct {
	executor  Executor
	tracer    trace.Tracer
	execMode  Opt[pgx.QueryExecMode]
	batchRows int
}

// Option configures a database before it is shared with callers.
type Option func(*DB)

// WithExecMode selects the pgx protocol mode for every query.
func WithExecMode(mode pgx.QueryExecMode) Option {
	return func(db *DB) { db.execMode = OptFrom(mode) }
}

// WithTracerProvider selects the provider used for database operation spans.
func WithTracerProvider(provider trace.TracerProvider) Option {
	return func(db *DB) { db.tracer = provider.Tracer("github.com/gopherex/ratel") }
}

// CallOption configures a single terminal operation.
type CallOption func(callSettings) callSettings

type callSettings struct{ mode Opt[pgx.QueryExecMode] }

// ExecMode overrides the database protocol mode for a terminal operation.
func ExecMode(mode pgx.QueryExecMode) CallOption {
	return func(settings callSettings) callSettings {
		settings.mode = OptFrom(mode)

		return settings
	}
}

// New constructs a database using the supplied executor and the global tracer provider.
func New(executor Executor, options ...Option) *DB {
	const defaultBatchRows = 1000

	database := &DB{
		executor: executor, tracer: otel.GetTracerProvider().Tracer("github.com/gopherex/ratel"),
		batchRows: defaultBatchRows,
	}
	for _, option := range options {
		option(database)
	}

	return database
}
