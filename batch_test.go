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

// bwResults replays scripted batch results, one per queued statement.
type bwResults struct {
	pgx.BatchResults
	rows     []*readRows
	tags     []string
	queryErr error
	execErr  error
	closeErr error
	closed   bool
}

func (r *bwResults) Query() (pgx.Rows, error) {
	if r.queryErr != nil {
		return nil, r.queryErr
	}

	rows := r.rows[0]
	r.rows = r.rows[1:]

	return rows, nil
}

func (r *bwResults) Exec() (pgconn.CommandTag, error) {
	if r.execErr != nil {
		return pgconn.CommandTag{}, r.execErr
	}

	tag := r.tags[0]
	r.tags = r.tags[1:]

	return pgconn.NewCommandTag(tag), nil
}

func (r *bwResults) Close() error {
	r.closed = true

	return r.closeErr
}

type bwBatchExecutor struct {
	ratel.Executor
	results *bwResults
	queued  *int
}

func (e bwBatchExecutor) SendBatch(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
	*e.queued = batch.Len()

	return e.results
}

func TestBwBatchOutcomes(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	writes := writeDescriptor()
	offline := errors.New("offline")
	closeFailure := errors.New("close failed")
	unique := &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}
	scanFailure := errors.New("scan failed")
	iterFailure := errors.New("iteration failed")

	type outcome struct {
		items func(db *ratel.DB) ([]ratel.BatchItem, func() bool)
		res   func() *bwResults
		want  error
		// queued is the statement count expected at the executor, -1 when nothing may be sent.
		queued int
	}

	for name, tc := range map[string]outcome{
		"empty batch": {
			items:  func(*ratel.DB) ([]ratel.BatchItem, func() bool) { return nil, nil },
			res:    func() *bwResults { return &bwResults{} },
			queued: -1,
		},
		"invalid select": {
			items: func(*ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all []readUser

				return []ratel.BatchItem{ratel.Select[readUser]{}.Batch(&all)}, nil
			},
			res: func() *bwResults { return &bwResults{} }, want: ratel.ErrInvalidQuery, queued: -1,
		},
		"invalid item after a valid one": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all, none []readUser

				return []ratel.BatchItem{db.From(users).Batch(&all), ratel.Select[readUser]{}.Batch(&none)}, nil
			},
			res: func() *bwResults { return &bwResults{} }, want: ratel.ErrInvalidQuery, queued: -1,
		},
		"invalid one": {
			items: func(*ratel.DB) ([]ratel.BatchItem, func() bool) {
				var one readUser

				return []ratel.BatchItem{ratel.Select[readUser]{}.BatchOne(&one)}, nil
			},
			res: func() *bwResults { return &bwResults{} }, want: ratel.ErrInvalidQuery, queued: -1,
		},
		"invalid write": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var count int64

				return []ratel.BatchItem{db.Update(writes).Set(writes.Email.To("a")).Batch(&count)}, nil
			},
			res: func() *bwResults { return &bwResults{} }, want: ratel.ErrInvalidQuery, queued: -1,
		},
		"invalid insert": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var count int64

				return []ratel.BatchItem{db.Insert(writes).Batch(&count)}, nil
			},
			res: func() *bwResults { return &bwResults{} }, want: ratel.ErrInvalidInsert, queued: -1,
		},
		"query failure": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all []readUser

				return []ratel.BatchItem{db.From(users).Batch(&all)}, nil
			},
			res: func() *bwResults { return &bwResults{queryErr: offline} }, want: offline, queued: 1,
		},
		"scan failure": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all []readUser

				return []ratel.BatchItem{db.From(users).Batch(&all)}, func() bool { return all == nil }
			},
			res: func() *bwResults {
				return &bwResults{rows: []*readRows{{ids: []int64{1}, scanFailure: scanFailure}}}
			},
			want: scanFailure, queued: 1,
		},
		"iteration failure": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all []readUser

				return []ratel.BatchItem{db.From(users).Batch(&all)}, func() bool { return all == nil }
			},
			res:  func() *bwResults { return &bwResults{rows: []*readRows{{failure: iterFailure}}} },
			want: iterFailure, queued: 1,
		},
		"one without rows": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var one readUser

				return []ratel.BatchItem{db.From(users).BatchOne(&one)}, func() bool { return one == readUser{} }
			},
			res:  func() *bwResults { return &bwResults{rows: []*readRows{{}}} },
			want: ratel.ErrNotFound, queued: 1,
		},
		"one with many rows": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var one readUser

				return []ratel.BatchItem{db.From(users).BatchOne(&one)}, func() bool { return one == readUser{} }
			},
			res:  func() *bwResults { return &bwResults{rows: []*readRows{{ids: []int64{1, 2}}}} },
			want: ratel.ErrMultipleRows, queued: 1,
		},
		"one query failure": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var one readUser

				return []ratel.BatchItem{db.From(users).BatchOne(&one)}, nil
			},
			res: func() *bwResults { return &bwResults{queryErr: offline} }, want: offline, queued: 1,
		},
		"exec item": {
			items: func(*ratel.DB) ([]ratel.BatchItem, func() bool) {
				var count int64

				item := ratel.ExecItem(ratel.Statement{SQL: "DELETE FROM app.users WHERE id = $1", Args: []any{1}}, &count)

				return []ratel.BatchItem{item}, func() bool { return count == 3 }
			},
			res: func() *bwResults { return &bwResults{tags: []string{"DELETE 3"}} }, queued: 1,
		},
		"exec item constraint": {
			items: func(*ratel.DB) ([]ratel.BatchItem, func() bool) {
				var count int64

				return []ratel.BatchItem{ratel.ExecItem(ratel.Statement{SQL: "INSERT INTO app.users"}, &count)},
					func() bool { return count == 0 }
			},
			res:  func() *bwResults { return &bwResults{execErr: unique} },
			want: &ratel.ConstraintError{Kind: ratel.ConstraintUnique, Constraint: "users_email_key"}, queued: 1,
		},
		"exec item technical failure": {
			items: func(*ratel.DB) ([]ratel.BatchItem, func() bool) {
				var count int64

				return []ratel.BatchItem{ratel.ExecItem(ratel.Statement{SQL: "DELETE FROM app.users"}, &count)}, nil
			},
			res: func() *bwResults { return &bwResults{execErr: offline} }, want: offline, queued: 1,
		},
		"close failure": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all []readUser

				return []ratel.BatchItem{db.From(users).Batch(&all)}, nil
			},
			res:  func() *bwResults { return &bwResults{rows: []*readRows{{ids: []int64{1}}}, closeErr: closeFailure} },
			want: closeFailure, queued: 1,
		},
		"item failure wins over close failure": {
			items: func(db *ratel.DB) ([]ratel.BatchItem, func() bool) {
				var all []readUser

				return []ratel.BatchItem{db.From(users).Batch(&all)}, nil
			},
			res:  func() *bwResults { return &bwResults{queryErr: offline, closeErr: closeFailure} },
			want: offline, queued: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			queued := -1
			results := tc.res()
			db := ratel.New(bwBatchExecutor{results: results, queued: &queued})
			items, verify := tc.items(db)

			err := db.Batch(t.Context(), items...)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Batch() = %v, want %v", err, tc.want)
			}

			if queued != tc.queued {
				t.Fatalf("queued = %d, want %d", queued, tc.queued)
			}

			if sent := tc.queued != -1; results.closed != sent {
				t.Fatalf("closed = %v, sent = %v", results.closed, sent)
			}

			if verify != nil && !verify() {
				t.Fatal("destination does not match the outcome")
			}
		})
	}
}

// TestBatchRowWrites checks that a row write in a batch refuses when it
// matches no row, as it does outside one.
func TestBatchRowWrites(t *testing.T) {
	t.Parallel()

	users := keyedDescriptor(true)
	user := readUser{ID: 7, Email: "a@x"}

	for _, tc := range []struct {
		name  string
		write func(*ratel.DB) ratel.Write[readUser]
		tag   string
		want  error
		count int64
	}{
		{
			"delete missing", func(db *ratel.DB) ratel.Write[readUser] { return db.DeleteRow(users, user) }, "DELETE 0",
			ratel.ErrNotFound, 0,
		},
		{"stale version", func(db *ratel.DB) ratel.Write[readUser] {
			return db.Save(users, user).ExpectVersion(users.Email, "old")
		}, "UPDATE 0", ratel.ErrStaleVersion, 0},
		{"saved", func(db *ratel.DB) ratel.Write[readUser] { return db.Save(users, user) }, "UPDATE 1", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queued := 0
			db := ratel.New(bwBatchExecutor{results: &bwResults{tags: []string{tc.tag}}, queued: &queued})

			var count int64

			if err := db.Batch(t.Context(), tc.write(db).Batch(&count)); !errors.Is(err, tc.want) || count != tc.count {
				t.Fatalf("Batch() = %v, count %d; want %v, %d", err, count, tc.want, tc.count)
			}
		})
	}
}

func TestBwBatchNilDB(t *testing.T) {
	t.Parallel()

	var db *ratel.DB

	if err := db.Batch(t.Context()); !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Batch() = %v", err)
	}
}

func TestBwBatchEmptyRowsKeepEmptySlice(t *testing.T) {
	t.Parallel()

	queued := 0
	db := ratel.New(bwBatchExecutor{results: &bwResults{rows: []*readRows{{}}}, queued: &queued})
	all := []readUser{{ID: 9}}

	if err := db.Batch(t.Context(), db.From(userDescriptor()).Batch(&all)); err != nil {
		t.Fatal(err)
	}

	if all == nil || len(all) != 0 {
		t.Fatalf("destination = %#v, want empty non-nil slice", all)
	}
}

func BenchmarkBwBatchOne(b *testing.B) {
	users := userDescriptor()

	for b.Loop() {
		queued := 0
		db := ratel.New(bwBatchExecutor{results: &bwResults{rows: []*readRows{{ids: []int64{1}}}}, queued: &queued})

		var one readUser
		if err := db.Batch(b.Context(), db.From(users).BatchOne(&one)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBwExecItem(b *testing.B) {
	statement := ratel.Statement{SQL: "DELETE FROM app.users WHERE id = $1", Args: []any{1}}

	for b.Loop() {
		queued := 0
		db := ratel.New(bwBatchExecutor{results: &bwResults{tags: []string{"DELETE 1"}}, queued: &queued})

		var count int64
		if err := db.Batch(b.Context(), ratel.ExecItem(statement, &count)); err != nil {
			b.Fatal(err)
		}
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
