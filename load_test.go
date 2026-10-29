package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

type relationRows struct {
	pgx.Rows
	values      [][]any
	index       int
	closed      bool
	failure     error
	scanFailure error
}

func (r *relationRows) Next() bool {
	if r.index == len(r.values) {
		return false
	}

	r.index++

	return true
}

func (r *relationRows) Scan(dest ...any) error {
	if r.scanFailure != nil {
		return r.scanFailure
	}

	for index, value := range r.values[r.index-1] {
		reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(value))
	}

	return nil
}

func (r *relationRows) Close()     { r.closed = true }
func (r *relationRows) Err() error { return r.failure }

type relationBatch struct {
	pgx.BatchResults
	rows         []*relationRows
	index        int
	closed       bool
	failure      error
	closeFailure error
}

func (r *relationBatch) Query() (pgx.Rows, error) {
	if r.failure != nil {
		return nil, r.failure
	}

	rows := r.rows[r.index]
	r.index++

	return rows, nil
}

func (r *relationBatch) Close() error {
	r.closed = true

	return errors.Join(r.failure, r.closeFailure)
}

type relationExecutor struct {
	readExecutor
	send func(context.Context, *pgx.Batch) pgx.BatchResults
}

func (e relationExecutor) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	return e.send(ctx, batch)
}

func relationItems(orders ratel.Table[relationOrder]) ratel.Rel[relationOrder, relationOrder] {
	return ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationOrder]{
		{Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relationOrder) any {
			values := make([]int64, len(models))
			for index, model := range models {
				values[index] = model.ID
			}

			return values
		}},
	}, func(model *relationOrder, children []relationOrder) { model.R.Items = ratel.LoadedFrom(children) },
		ratel.RelCount[relationOrder, relationOrder](func(model *relationOrder, count int64) {
			model.R.ItemCount = ratel.LoadedFrom(count)
		}))
}

func TestLoadBreadthFirst(t *testing.T) {
	t.Parallel()

	users, orders := relationDescriptors()
	items := relationItems(orders)
	batches := []*relationBatch{
		{rows: []*relationRows{
			{values: [][]any{{1, int64(11), int64(1)}, {1, int64(12), int64(1)}}},
			{values: [][]any{{1, int64(2)}}},
		}},
		{rows: []*relationRows{
			{values: [][]any{{1, int64(111), int64(11)}}},
			{values: [][]any{{1, int64(1)}}},
		}},
	}
	calls := 0
	executor := relationExecutor{send: func(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
		if calls > 0 && !batches[calls-1].closed {
			t.Fatal("previous depth batch still open")
		}
		if batch.Len() != len(batches[calls].rows) {
			t.Fatalf("depth %d: %d queries", calls, batch.Len())
		}
		if !strings.Contains(batch.QueuedQueries[0].SQL, "ANY($1)") {
			t.Fatal(batch.QueuedQueries[0].SQL)
		}
		result := batches[calls]
		calls++

		return result
	}}
	models := []relationUser{{ID: 1}, {ID: 2}}

	err := ratel.New(executor).Load(t.Context(), models, users.Orders.With(items).WithCount(items), users.Orders.Count())
	if err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("depth batches=%d", calls)
	}

	assertLoadedGraph(t, models)
}

func assertLoadedGraph(t *testing.T, models []relationUser) {
	t.Helper()

	loaded, err := models[0].Orders()
	if err != nil || len(loaded) != 2 {
		t.Fatalf("%+v %v", loaded, err)
	}

	nested, err := loaded[0].R.Items.Get()
	if err != nil || len(nested) != 1 || nested[0].ID != 111 {
		t.Fatalf("%+v %v", nested, err)
	}

	nestedCount, err := loaded[0].R.ItemCount.Get()
	if err != nil || nestedCount != 1 {
		t.Fatalf("nested count=%d err=%v", nestedCount, err)
	}

	empty, err := models[1].Orders()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty=%v err=%v", empty, err)
	}

	count, err := models[0].R.OrderCount.Get()
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestLoadFailureAtomicAttachment(t *testing.T) {
	t.Parallel()

	failure := errors.New("connection lost")
	for _, tc := range []struct {
		name   string
		result *relationBatch
		want   error
	}{
		{"query", &relationBatch{failure: failure}, failure},
		{"scan", &relationBatch{rows: []*relationRows{{values: [][]any{{1}}, scanFailure: failure}}}, failure},
		{"iterate", &relationBatch{rows: []*relationRows{{failure: failure}}}, failure},
		{"close", &relationBatch{rows: []*relationRows{{}}, closeFailure: failure}, failure},
		{
			name: "invalid ordinal", want: ratel.ErrInvalidQuery,
			result: &relationBatch{rows: []*relationRows{{values: [][]any{{0, int64(1), int64(1)}}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults { return tc.result }}
			models := []relationUser{{ID: 1}, {ID: 2}}

			err := ratel.New(executor).Load(t.Context(), models, users.Orders)
			if !errors.Is(err, tc.want) || !tc.result.closed {
				t.Fatalf("err=%v closed=%v", err, tc.result.closed)
			}

			if _, err := models[0].Orders(); !errors.Is(err, ratel.ErrNotLoaded) {
				t.Fatal("attached failed load")
			}
		})
	}
}

// rowOrderByParent is the child ids of relation rows (parent ordinal, id,
// user id) grouped by parent, each parent's ids in row order.
func rowOrderByParent(rows [][]any) []int64 {
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(left, right []any) int {
		leftParent, _ := left[0].(int)
		rightParent, _ := right[0].(int)

		return leftParent - rightParent
	})

	ids := make([]int64, 0, len(sorted))

	for _, row := range sorted {
		id, _ := row[1].(int64)
		ids = append(ids, id)
	}

	return ids
}

// assertAttached checks each parent's children: their count, a capacity that
// ends at the parent's own children, and the child ids in order.
func assertAttached(t *testing.T, models []relationUser, counts []int, want []int64) {
	t.Helper()

	var got []int64

	for index, count := range counts {
		children, err := models[index].Orders()
		if err != nil || children == nil || len(children) != count || cap(children) != count {
			t.Fatalf("parent %d: children=%v capacity=%d err=%v", index, children, cap(children), err)
		}

		for _, child := range children {
			got = append(got, child.ID)
		}
	}

	if !slices.Equal(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
}

func TestLoadGroupOwnership(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		rows   [][]any
		counts []int
	}{
		{"empty", nil, []int{0, 0}},
		{"dense", [][]any{{1, int64(11), int64(1)}, {2, int64(21), int64(2)}}, []int{1, 1}},
		{"sparse", [][]any{
			{1, int64(11), int64(1)},
			{1, int64(12), int64(1)},
			{3, int64(31), int64(3)},
		}, []int{2, 0, 1}},
		// Rows arrive in any parent order; each parent keeps its rows in row
		// order, the child order the SQL requested.
		{"ungrouped", [][]any{
			{3, int64(32), int64(3)},
			{1, int64(12), int64(1)},
			{3, int64(31), int64(3)},
			{1, int64(11), int64(1)},
			{3, int64(33), int64(3)},
		}, []int{2, 0, 3}},
		{"grouped then not", [][]any{
			{1, int64(11), int64(1)},
			{1, int64(12), int64(1)},
			{2, int64(21), int64(2)},
			{3, int64(31), int64(3)},
			{1, int64(13), int64(1)},
			{2, int64(22), int64(2)},
		}, []int{3, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			models := make([]relationUser, len(tc.counts))
			expected := make([]int64, len(models))
			executor := relationExecutor{send: func(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
				if !reflect.DeepEqual(batch.QueuedQueries[0].Arguments[0], expected) {
					t.Fatalf("stale parent keys: %v", batch.QueuedQueries[0].Arguments)
				}

				return &relationBatch{rows: []*relationRows{{values: tc.rows}}}
			}}
			database := ratel.New(executor)

			for round := range 2 {
				for index := range models {
					expected[index] = int64(round*len(models) + index + 1)
					models[index].ID = expected[index]
				}

				if err := database.Load(t.Context(), models, users.Orders); err != nil {
					t.Fatal(err)
				}

				assertAttached(t, models, tc.counts, rowOrderByParent(tc.rows))
			}
		})
	}
}

func TestLoadCounts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		rows    [][]any
		want    int64
		invalid bool
	}{
		{name: "missing group"},
		{name: "present", rows: [][]any{{1, int64(3)}}, want: 3},
		{name: "invalid parent", rows: [][]any{{2, int64(3)}}, invalid: true},
		{name: "negative count", rows: [][]any{{1, int64(-1)}}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
				return &relationBatch{rows: []*relationRows{{values: tc.rows}}}
			}}
			models := []relationUser{{ID: 1}}

			err := ratel.New(executor).Load(t.Context(), models, users.Orders.Count())
			if tc.invalid {
				if !errors.Is(err, ratel.ErrInvalidQuery) {
					t.Fatal(err)
				}

				if !models[0].R.OrderCount.IsZero() {
					t.Fatal("attached invalid count")
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			count, err := models[0].R.OrderCount.Get()
			if err != nil || count != tc.want {
				t.Fatalf("count=%d err=%v", count, err)
			}

			if _, failure := models[0].Orders(); !errors.Is(failure, ratel.ErrNotLoaded) {
				t.Fatal("count loaded rows")
			}
		})
	}
}

func TestLoadDeepFailurePreservesExisting(t *testing.T) {
	t.Parallel()

	users, orders := relationDescriptors()
	failure := errors.New("nested connection lost")
	calls := 0
	batches := []*relationBatch{
		{rows: []*relationRows{{values: [][]any{{1, int64(11), int64(1)}}}}},
		{failure: failure},
	}
	executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
		result := batches[calls]
		calls++

		return result
	}}
	original := []relationOrder{{ID: 99}}
	models := []relationUser{{ID: 1, R: relationUserRelations{Orders: ratel.LoadedFrom(original)}}}

	err := ratel.New(executor).Load(t.Context(), models, users.Orders.With(relationItems(orders)))
	if !errors.Is(err, failure) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	loaded, err := models[0].Orders()
	if err != nil || !reflect.DeepEqual(loaded, original) {
		t.Fatalf("changed original: %v %v", loaded, err)
	}

	for _, batch := range batches {
		if !batch.closed {
			t.Fatal("batch still open")
		}
	}
}

// TestLoadOutcomes checks that a technical failure, including one closing
// the batch after a cardinality refusal, reaches the caller.
func TestLoadOutcomes(t *testing.T) {
	t.Parallel()

	failure := errors.New("driver failure")
	for _, tc := range []struct {
		name         string
		values       [][]any
		failure      error
		closeFailure error
		want         error
	}{
		{name: "success", values: [][]any{{1, int64(11), int64(1)}}},
		{name: "technical", failure: failure, want: failure},
		{
			name: "cardinality refusal", values: [][]any{{1, int64(11), int64(1)}, {1, int64(12), int64(1)}},
			want: ratel.ErrMultipleRows,
		},
		{
			name: "refusal followed by technical close", values: [][]any{{1, int64(11), int64(1)}, {1, int64(12), int64(1)}},
			closeFailure: failure, want: failure,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, orders := relationDescriptors()
			relation := ratel.NewRel(orders, ratel.RelOne, []ratel.RelKey[relationUser]{
				{
					Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"},
					Values: func(models []relationUser) any {
						values := make([]int64, len(models))
						for index, model := range models {
							values[index] = model.ID
						}

						return values
					},
				},
			}, func(model *relationUser, children []relationOrder) { model.R.Orders = ratel.LoadedFrom(children) })
			executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
				return &relationBatch{
					rows: []*relationRows{{values: tc.values}}, failure: tc.failure, closeFailure: tc.closeFailure,
				}
			}}

			err := ratel.New(executor).Load(t.Context(), []relationUser{{ID: 1}}, relation)
			if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
				t.Fatalf("Load() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBwLoadRejects(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	failure := errors.New("offline")
	nested := users.Orders.With(ratel.Relation[relationOrder](nil))

	// narrow scans fewer values than the table declares columns.
	narrow := ratel.NewTable("app", "users", []string{"id", "email"}, func(row pgx.Row) (readUser, error) {
		var user readUser

		err := row.Scan(&user.ID)

		return user, err
	}, ratel.PrimaryKey([]string{"id"}, func(user readUser) []any { return []any{user.ID} }))
	arity := ratel.NewRel(narrow, ratel.RelMany, []ratel.RelKey[readUser]{{
		Column: "email", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func([]readUser) any { return []int64{1} },
	}}, func(*readUser, []readUser) {})

	for _, tc := range []struct {
		name  string
		rows  []*relationRows
		load  func(*ratel.DB) error
		want  error
		first bool // the first depth runs and must close
	}{
		{
			name: "count scan failure", rows: []*relationRows{{values: [][]any{{1, int64(3)}}, scanFailure: failure}},
			load: func(db *ratel.DB) error {
				return db.Load(t.Context(), []relationUser{{ID: 1}}, users.Orders.Count())
			},
			want: failure, first: true,
		},
		{
			name: "count iteration failure", rows: []*relationRows{{failure: failure}},
			load: func(db *ratel.DB) error {
				return db.Load(t.Context(), []relationUser{{ID: 1}}, users.Orders.Count())
			},
			want: failure, first: true,
		},
		{
			name: "nil nested relation", rows: []*relationRows{{values: [][]any{{1, int64(11), int64(1)}}}},
			load: func(db *ratel.DB) error {
				return db.Load(t.Context(), []relationUser{{ID: 1}}, nested)
			},
			want: ratel.ErrInvalidQuery, first: true,
		},
		{
			name: "scanner reads fewer columns than selected", rows: []*relationRows{{values: [][]any{{1, int64(1), "x"}}}},
			load: func(db *ratel.DB) error { return db.Load(t.Context(), []readUser{{ID: 1}}, arity) },
			want: ratel.ErrInvalidQuery, first: true,
		},
		{
			name: "nil relation",
			load: func(db *ratel.DB) error {
				return db.Load(t.Context(), []relationUser{{ID: 1}}, ratel.Relation[relationUser](nil))
			},
			want: ratel.ErrInvalidQuery,
		},
		{
			name: "no database",
			load: func(*ratel.DB) error {
				var missing *ratel.DB

				return missing.Load(t.Context(), []relationUser{{ID: 1}}, users.Orders)
			},
			want: ratel.ErrInvalidQuery,
		},
		{
			name: "no models sends nothing",
			load: func(db *ratel.DB) error { return db.Load(t.Context(), []relationUser{}, users.Orders) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			batch := &relationBatch{rows: tc.rows}
			sent := 0
			executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
				sent++

				return batch
			}}

			if err := tc.load(ratel.New(executor)); !errors.Is(err, tc.want) {
				t.Fatalf("Load() = %v, want %v", err, tc.want)
			}

			if tc.first && (sent != 1 || !batch.closed) {
				t.Fatalf("depth batches = %d, closed = %v", sent, batch.closed)
			}

			if !tc.first && sent != 0 {
				t.Fatalf("sent %d batches", sent)
			}
		})
	}
}

func BenchmarkLoad(b *testing.B) {
	users, _ := relationDescriptors()
	executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
		return &relationBatch{rows: []*relationRows{{values: [][]any{{1, int64(11), int64(1)}}}}}
	}}
	database := ratel.New(executor)
	models := []relationUser{{ID: 1}}

	b.ReportAllocs()

	for b.Loop() {
		if err := database.Load(b.Context(), models, users.Orders); err != nil {
			b.Fatal(err)
		}
	}
}
