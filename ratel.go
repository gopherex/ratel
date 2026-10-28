// Package ratel provides PostgreSQL data access over a caller-owned executor.
package ratel

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	executor Executor
}

// New constructs a database using the supplied executor.
func New(executor Executor) *DB {
	return &DB{executor: executor}
}
