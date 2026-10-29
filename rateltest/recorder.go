package rateltest

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// Recorder is a concurrency-safe pgx QueryTracer, BatchTracer and CopyFromTracer.
// Assign it to poolConfig.ConnConfig.Tracer before creating the pool. It records
// attempted statements, including failures, exactly once. Arguments are shallow
// copies: mutable argument values retain pgx's caller-owned lifetime contract.
// SQL and arguments may contain secrets; the recorder never logs either.
type Recorder struct {
	mutex   sync.Mutex
	queries []Query
}

// Query is one statement the recorder saw: its SQL text and ordered arguments.
type Query struct {
	SQL  string
	Args []any
}

// Queries returns an independent snapshot in trace-start order.
func (recorder *Recorder) Queries() []Query {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	queries := slices.Clone(recorder.queries)
	for index := range queries {
		queries[index].Args = slices.Clone(queries[index].Args)
	}

	return queries
}

// Reset clears recorded queries without changing the tracer configuration.
func (recorder *Recorder) Reset() {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	recorder.queries = nil
}

func (recorder *Recorder) record(sql string, args []any) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	recorder.queries = append(recorder.queries, Query{SQL: sql, Args: slices.Clone(args)})
}

// TraceQueryStart records one attempted query.
func (recorder *Recorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	recorder.record(data.SQL, data.Args)

	return ctx
}

// TraceQueryEnd completes the pgx QueryTracer contract.
func (*Recorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TraceBatchStart records each queued statement once, including failed batches.
func (recorder *Recorder) TraceBatchStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceBatchStartData,
) context.Context {
	for _, query := range data.Batch.QueuedQueries {
		recorder.record(query.SQL, query.Arguments)
	}

	return ctx
}

// TraceBatchQuery does not double-count statements recorded at batch start.
func (*Recorder) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

// TraceBatchEnd completes the pgx BatchTracer contract.
func (*Recorder) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

// TraceCopyFromStart records one COPY command, without consuming its row source.
func (recorder *Recorder) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceCopyFromStartData,
) context.Context {
	columns := make([]string, len(data.ColumnNames))
	for index, name := range data.ColumnNames {
		columns[index] = pgx.Identifier{name}.Sanitize()
	}

	recorder.record("COPY "+data.TableName.Sanitize()+" ("+strings.Join(columns, ", ")+") FROM STDIN", nil)

	return ctx
}

// TraceCopyFromEnd completes the pgx CopyFromTracer contract.
func (*Recorder) TraceCopyFromEnd(context.Context, *pgx.Conn, pgx.TraceCopyFromEndData) {}
