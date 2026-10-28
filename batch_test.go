package ratel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel"
)

// batchResults replays one result per queued statement: rows for queries,
// a command tag for execs.
type batchResults struct {
	queries [][]int64
	tags    []string
	failure error
	closed  bool
}

func (r *batchResults) Exec() (pgconn.CommandTag, error) {
	if r.failure != nil {
		return pgconn.CommandTag{}, r.failure
	}

	tag := r.tags[0]
	r.tags = r.tags[1:]

	return pgconn.NewCommandTag(tag), nil
}

func (r *batchResults) Query() (pgx.Rows, error) {
	ids := r.queries[0]
	r.queries = r.queries[1:]

	return &readRows{ids: ids}, nil
}

func (r *batchResults) QueryRow() pgx.Row { return nil }

func (r *batchResults) Close() error {
	r.closed = true

	return nil
}

type batchExecutor struct {
	ratel.Executor
	results *batchResults
	queued  *int
}

func (e batchExecutor) SendBatch(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
	*e.queued = batch.Len()

	return e.results
}

func TestBatch(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	writes := writeDescriptor()
	unique := &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}

	for _, tc := range []struct {
		name    string
		results *batchResults
		queued  int
		want    error
	}{
		{
			name: "all", queued: 4,
			results: &batchResults{queries: [][]int64{{1, 2}, {3}}, tags: []string{"UPDATE 2", "INSERT 0 1"}},
		},
		{
			name: "constraint", results: &batchResults{queries: [][]int64{{1}, {3}}, failure: unique}, queued: 4,
			want: &ratel.ConstraintError{Kind: ratel.ConstraintUnique},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queued := 0
			database := ratel.New(batchExecutor{results: tc.results, queued: &queued})

			var (
				all     []readUser
				one     readUser
				updated int64
				added   int64
			)

			err := database.Batch(t.Context(),
				database.From(users).Batch(&all),
				database.From(users).Where(users.ID.Eq(3)).BatchOne(&one),
				database.Update(writes).Set(writes.Email.To("a")).All().Batch(&updated),
				database.Insert(writes, newWriteInsert("b")).Batch(&added),
			)

			if !errors.Is(err, tc.want) || queued != tc.queued || !tc.results.closed {
				t.Fatalf("Batch() = %v, queued %d, closed %v", err, queued, tc.results.closed)
			}

			if tc.want == nil && (len(all) != 2 || one.ID != 3 || updated != 2 || added != 1) {
				t.Fatalf("destinations = %v, %v, %d, %d", all, one, updated, added)
			}
		})
	}
}

func TestBatchInvalid(t *testing.T) {
	t.Parallel()

	queued := -1
	db := ratel.New(batchExecutor{results: &batchResults{}, queued: &queued})

	var count int64

	err := db.Batch(t.Context(), ratel.ExecItem(ratel.Statement{}, &count))
	if !errors.Is(err, ratel.ErrInvalidQuery) || queued != -1 {
		t.Fatalf("Batch() = %v, sent = %v", err, queued != -1)
	}
}

func BenchmarkBatch(b *testing.B) {
	users := userDescriptor()

	for b.Loop() {
		queued := 0
		db := ratel.New(batchExecutor{results: &batchResults{queries: [][]int64{{1}}}, queued: &queued})

		var all []readUser
		if err := db.Batch(b.Context(), db.From(users).Batch(&all)); err != nil {
			b.Fatal(err)
		}
	}
}
