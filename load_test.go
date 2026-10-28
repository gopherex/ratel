package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace/noop"

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
		{
			name: "unordered ordinals", want: ratel.ErrInvalidQuery,
			result: &relationBatch{rows: []*relationRows{{values: [][]any{
				{2, int64(2), int64(2)}, {1, int64(1), int64(1)},
			}}}},
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

				for index, count := range tc.counts {
					children, err := models[index].Orders()
					if err != nil || children == nil || len(children) != count || cap(children) != count {
						t.Fatalf("parent %d: children=%v capacity=%d err=%v", index, children, cap(children), err)
					}
				}
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

func TestLoadTelemetry(t *testing.T) {
	t.Parallel()

	failure := errors.New("sensitive driver failure")
	for _, tc := range []struct {
		name         string
		values       [][]any
		failure      error
		closeFailure error
		want         error
		status       codes.Code
	}{
		{name: "success", values: [][]any{{1, int64(11), int64(1)}}, status: codes.Unset},
		{name: "technical", failure: failure, want: failure, status: codes.Error},
		{
			name: "cardinality refusal", values: [][]any{{1, int64(11), int64(1)}, {1, int64(12), int64(1)}},
			want: ratel.ErrMultipleRows, status: codes.Unset,
		},
		{
			name: "refusal followed by technical close", values: [][]any{{1, int64(11), int64(1)}, {1, int64(12), int64(1)}},
			closeFailure: failure, want: failure, status: codes.Error,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var spans []*observedSpan
			provider := observedProvider{TracerProvider: noop.NewTracerProvider(), spans: &spans}
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

			err := ratel.New(executor, ratel.WithTracerProvider(provider)).Load(t.Context(), []relationUser{{ID: 1}}, relation)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}

			if len(spans) != 2 {
				t.Fatalf("spans=%d", len(spans))
			}

			assertLoadSpanStatus(t, spans, tc.status)
		})
	}
}

func assertLoadSpanStatus(t *testing.T, spans []*observedSpan, status codes.Code) {
	t.Helper()

	for _, span := range spans {
		if !span.ended || span.status != status {
			t.Fatalf("ended=%v status=%v", span.ended, span.status)
		}

		attributes := make(map[string]string)
		for _, attribute := range span.attributes {
			attributes[string(attribute.Key)] = attribute.Value.AsString()
		}

		expected := map[string]string{"db.system.name": "postgresql", "db.operation.name": "SELECT"}
		if !reflect.DeepEqual(attributes, expected) {
			t.Fatalf("database span attributes: %v", attributes)
		}
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
