package rateltest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/rateltest"
)

func TestRecorder(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{"SELECT $1", "UPDATE app.users SET active = true"} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			recorder := &rateltest.Recorder{}
			args := []any{int64(7)}
			ctx := recorder.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
			recorder.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

			args[0] = int64(8)

			got := recorder.Statements()
			if len(got) != 1 || got[0].SQL != sql || got[0].Args[0] != int64(7) {
				t.Fatalf("statements = %v", got)
			}

			got[0].SQL = "mutated"
			got[0].Args[0] = 9

			if recorder.Statements()[0].Args[0] != int64(7) || recorder.Statements()[0].SQL != sql {
				t.Fatal("snapshot aliases recorder")
			}

			recorder.Reset()

			if len(recorder.Statements()) != 0 {
				t.Fatal("reset did not clear")
			}
		})
	}
}

func TestRecorderBatchAndCopy(t *testing.T) {
	t.Parallel()

	recorder := &rateltest.Recorder{}
	batch := &pgx.Batch{}
	batch.Queue("SELECT $1", 1)
	batch.Queue("SELECT $1", 2)
	ctx := recorder.TraceBatchStart(t.Context(), nil, pgx.TraceBatchStartData{Batch: batch})
	recorder.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{})
	recorder.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})
	ctx = recorder.TraceCopyFromStart(t.Context(), nil, pgx.TraceCopyFromStartData{
		TableName: pgx.Identifier{"app", "values"}, ColumnNames: []string{"v"},
	})
	recorder.TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{})

	got := recorder.Statements()
	if len(got) != 3 || got[1].Args[0] != 2 || got[2].SQL != `COPY "app"."values" ("v") FROM STDIN` {
		t.Fatalf("statements = %v", got)
	}
}

func TestRecorderConcurrent(t *testing.T) {
	t.Parallel()

	recorder := &rateltest.Recorder{}

	var group sync.WaitGroup
	for range 10 {
		group.Go(func() {
			recorder.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
			_ = recorder.Statements()
		})
	}

	group.Wait()

	if len(recorder.Statements()) != 10 {
		t.Fatal("lost statements")
	}
}

func BenchmarkRecorder(b *testing.B) {
	recorder := &rateltest.Recorder{}
	for b.Loop() {
		recorder.TraceQueryStart(b.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{1}})
		_ = recorder.Statements()
		recorder.Reset()
	}
}
