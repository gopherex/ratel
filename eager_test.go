package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestEagerSelect(t *testing.T) {
	t.Parallel()

	for _, terminal := range []string{"list", "one", "first", "chunks", "count"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			testEagerTerminal(t, terminal)
		})
	}
}

func testEagerTerminal(t *testing.T, terminal string) {
	t.Helper()

	users, _ := relationDescriptors()
	parentRows := &readRows{ids: []int64{1}}
	batch := &relationBatch{rows: []*relationRows{{values: [][]any{{1, int64(11), int64(1)}}}}}
	calls := 0
	executor := relationExecutor{
		readExecutor: readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return parentRows, nil
		}},
		send: func(_ context.Context, queued *pgx.Batch) pgx.BatchResults {
			calls++
			if !parentRows.closed {
				t.Fatal("parent cursor still open")
			}
			if terminal == "count" {
				batch.rows[0].values = [][]any{{1, int64(3)}}
			}
			if !reflect.DeepEqual(queued.QueuedQueries[0].Arguments[0], []int64{1}) {
				t.Fatal("batch key projection is not a native array")
			}

			return batch
		},
	}
	database := ratel.New(executor)
	query := ratel.From(users).With(users.Orders).
		Where(users.ID.Eq(1)).OrderBy(users.ID.Asc()).Limit(1).Offset(0)

	if terminal == "count" {
		query = ratel.From(users).WithCount(users.Orders)
	}

	models, err := eagerTerminal(t.Context(), database, query, terminal)
	if err != nil || len(models) != 1 || calls != 1 {
		t.Fatalf("models=%v calls=%d err=%v", models, calls, err)
	}

	if terminal == "count" {
		count, failure := models[0].R.OrderCount.Get()
		if failure != nil || count != 3 {
			t.Fatalf("%d %v", count, failure)
		}

		return
	}

	orders, failure := models[0].Orders()
	if failure != nil || len(orders) != 1 {
		t.Fatalf("%v %v", orders, failure)
	}
}

func eagerTerminal(
	ctx context.Context, database *ratel.DB, query ratel.EagerSelect[relationUser], terminal string,
) ([]relationUser, error) {
	switch terminal {
	case "one":
		model, err := database.Run(query).One(ctx)

		return []relationUser{model}, err
	case "first":
		model, err := database.Run(query).First(ctx)

		return []relationUser{model}, err
	case "chunks":
		for page, err := range database.Run(query).Chunks(ctx, 1) {
			return page, err
		}

		return nil, nil
	default:
		return database.Run(query).List(ctx)
	}
}

func TestEagerNoAll(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()

	query := ratel.From(users).With(users.Orders)
	if _, exists := reflect.TypeOf(ratel.New(readExecutor{}).Run(query)).MethodByName("All"); exists {
		t.Fatal("With exposes All")
	}
}

func TestEagerBuildRootOnly(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	plain := ratel.From(users).Where(users.ID.Gt(1)).Limit(3)

	want, err := build(plain)
	if err != nil {
		t.Fatal(err)
	}

	for name, eager := range map[string]ratel.EagerSelect[relationUser]{
		"with": plain.With(users.Orders), "with count": plain.WithCount(users.Orders),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := build(eager)
			if err != nil || got.SQL != want.SQL || !reflect.DeepEqual(got.Args, want.Args) {
				t.Fatalf("Build() = %+v, %v; want the root query %+v", got, err, want)
			}
		})
	}
}

func TestEagerFailure(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	failure := errors.New("offline")
	executor := relationExecutor{
		readExecutor: readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return &readRows{ids: []int64{1}}, nil
		}},
		send: func(context.Context, *pgx.Batch) pgx.BatchResults { return &relationBatch{failure: failure} },
	}

	result, err := ratel.New(executor).Run(ratel.From(users).With(users.Orders)).List(t.Context())
	if result != nil || !errors.Is(err, failure) {
		t.Fatalf("%v %v", result, err)
	}
}

func TestEagerInvalidRelation(t *testing.T) {
	t.Parallel()

	for _, terminal := range []string{"list", "one", "first", "chunks", "nil count"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
				t.Fatal("invalid relation reached parent executor")

				return nil, ratel.ErrInvalidQuery
			}}
			database := ratel.New(executor)
			query := ratel.From(users).With(ratel.Rel[relationUser, relationOrder]{})

			if terminal == "nil count" {
				query = ratel.From(users).WithCount(nil)
			}

			_, err := eagerTerminal(t.Context(), database, query, terminal)
			if !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatal(err)
			}
		})
	}
}

func TestEagerExecModeRejected(t *testing.T) {
	t.Parallel()

	for _, terminal := range []string{"list", "one", "first", "chunks", "load", "override"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			executor := relationExecutor{
				readExecutor: readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
					t.Fatal("invalid mode reached executor")

					return nil, ratel.ErrInvalidQuery
				}},
				send: func(context.Context, *pgx.Batch) pgx.BatchResults {
					t.Fatal("invalid mode reached batch")

					return nil
				},
			}
			database := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeExec))
			query := ratel.From(users).With(users.Orders)

			var err error

			switch terminal {
			case "load":
				err = database.Load(t.Context(), []relationUser{{ID: 1}}, users.Orders)
			case "override":
				_, err = ratel.New(executor).Run(ratel.From(users).With(users.Orders)).
					List(t.Context(), ratel.ExecMode(pgx.QueryExecModeExec))
			default:
				_, err = eagerTerminal(t.Context(), database, query, terminal)
			}

			if !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("mode: %v", err)
			}
		})
	}
}

func BenchmarkRelationBuilder(b *testing.B) {
	executor := relationExecutor{
		readExecutor: readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return &readRows{ids: []int64{1}}, nil
		}},
		send: func(context.Context, *pgx.Batch) pgx.BatchResults {
			return &relationBatch{rows: []*relationRows{{}, {}}}
		},
	}
	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		users, orders := relationDescriptors()
		id := ratel.NewCol[relationOrder, int64]("id")
		relation := users.Orders.Where(id.Gt(0)).OrderBy(id.Desc()).Limit(5).Offset(0).
			With(relationItems(orders)).Strategy(ratel.Window())

		query := ratel.From(users).With(relation).WithCount(users.Orders)
		if _, err := database.Run(query).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

var errEagerParent = errors.New("parent offline")

// eagerXParents serves parent rows and records each parent statement.
type eagerXParents struct {
	ids   []int64
	err   error
	calls int
	sql   string
	args  []any
}

func (p *eagerXParents) query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	p.calls++
	p.sql, p.args = sql, args

	if p.err != nil {
		return nil, p.err
	}

	return &readRows{ids: p.ids}, nil
}

// eagerXBatch answers every queued depth statement with one orders row and one count row.
func eagerXBatch(queued *pgx.Batch) *relationBatch {
	rows := make([]*relationRows, len(queued.QueuedQueries))

	for index, query := range queued.QueuedQueries {
		if strings.Contains(query.SQL, "count(*)") {
			rows[index] = &relationRows{values: [][]any{{1, int64(3)}}}
		} else {
			rows[index] = &relationRows{values: [][]any{{1, int64(11), int64(1)}}}
		}
	}

	return &relationBatch{rows: rows}
}

func TestEagerWithAppends(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()

	for _, tc := range []struct {
		name       string
		build      func(ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser]
		statements int
		orders     bool
		count      bool
	}{
		{
			name:       "unchanged base",
			build:      func(q ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] { return q },
			statements: 1, orders: true,
		},
		{
			name: "with appends relation",
			build: func(q ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] {
				return q.With(users.Orders.Limit(1))
			},
			statements: 2, orders: true,
		},
		{
			name: "with count appends count",
			build: func(q ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] {
				return q.WithCount(users.Orders)
			},
			statements: 2, orders: true, count: true,
		},
		{
			name: "with nothing",
			build: func(q ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] {
				return q.With().WithCount()
			},
			statements: 1, orders: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parents := &eagerXParents{ids: []int64{1}}
			statements := 0
			executor := relationExecutor{
				readExecutor: readExecutor{query: parents.query},
				send: func(_ context.Context, queued *pgx.Batch) pgx.BatchResults {
					statements = len(queued.QueuedQueries)

					return eagerXBatch(queued)
				},
			}
			base := ratel.From(users).With(users.Orders)

			models, err := ratel.New(executor).Run(tc.build(base)).List(t.Context())
			if err != nil || len(models) != 1 || statements != tc.statements {
				t.Fatalf("models=%v statements=%d err=%v", models, statements, err)
			}

			orders, failure := models[0].Orders()
			if tc.orders && (failure != nil || len(orders) != 1) {
				t.Fatalf("orders=%v err=%v", orders, failure)
			}

			count, failure := models[0].R.OrderCount.Get()
			if tc.count && (failure != nil || count != 3) {
				t.Fatalf("count=%d err=%v", count, failure)
			}

			if !tc.count && !errors.Is(failure, ratel.ErrNotLoaded) {
				t.Fatalf("count loaded unexpectedly: %d %v", count, failure)
			}

			// The base builder is not changed by any branch.
			statements = 0

			if _, err := ratel.New(executor).Run(base).List(t.Context()); err != nil || statements != 1 {
				t.Fatalf("base statements=%d err=%v", statements, err)
			}
		})
	}
}

func TestEagerWithCountOnSelect(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	parents := &eagerXParents{ids: []int64{1}}
	executor := relationExecutor{
		readExecutor: readExecutor{query: parents.query},
		send: func(_ context.Context, queued *pgx.Batch) pgx.BatchResults {
			if len(queued.QueuedQueries) != 2 || !strings.Contains(queued.QueuedQueries[1].SQL, "count(*)") {
				t.Fatalf("statements: %v", queued.QueuedQueries)
			}

			return eagerXBatch(queued)
		},
	}

	counted := ratel.From(users).WithCount(users.Orders).WithCount(users.Orders)

	models, err := ratel.New(executor).Run(counted).List(t.Context())
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%v err=%v", models, err)
	}

	if count, failure := models[0].R.OrderCount.Get(); failure != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, failure)
	}
}

func TestEagerSingle(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	limit := func(count int) func(ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] {
		return func(q ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] { return q.Limit(count) }
	}
	unlimited := func(q ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser] { return q }

	for _, tc := range []struct {
		name       string
		terminal   string
		prepare    func(ratel.EagerSelect[relationUser]) ratel.EagerSelect[relationUser]
		parents    eagerXParents
		batchError error
		wantErr    error
		wantLimit  any
		wantBatch  int
	}{
		{
			name: "first adds limit 1", terminal: "first", prepare: unlimited,
			parents: eagerXParents{ids: []int64{1}}, wantLimit: 1, wantBatch: 1,
		},
		{
			name: "one adds limit 2", terminal: "one", prepare: unlimited,
			parents: eagerXParents{ids: []int64{1}}, wantLimit: 2, wantBatch: 1,
		},
		{
			name: "first lowers larger limit", terminal: "first", prepare: limit(9),
			parents: eagerXParents{ids: []int64{1}}, wantLimit: 1, wantBatch: 1,
		},
		{
			name: "one lowers larger limit", terminal: "one", prepare: limit(9),
			parents: eagerXParents{ids: []int64{1}}, wantLimit: 2, wantBatch: 1,
		},
		{
			name: "one keeps smaller limit", terminal: "one", prepare: limit(1),
			parents: eagerXParents{ids: []int64{1}}, wantLimit: 1, wantBatch: 1,
		},
		{
			name: "first not found", terminal: "first", prepare: unlimited,
			parents: eagerXParents{}, wantErr: ratel.ErrNotFound, wantLimit: 1,
		},
		{
			name: "one not found", terminal: "one", prepare: unlimited,
			parents: eagerXParents{}, wantErr: ratel.ErrNotFound, wantLimit: 2,
		},
		{
			name: "one multiple rows", terminal: "one", prepare: unlimited,
			parents: eagerXParents{ids: []int64{1, 2}}, wantErr: ratel.ErrMultipleRows, wantLimit: 2,
		},
		{
			name: "first parent failure", terminal: "first", prepare: unlimited,
			parents: eagerXParents{err: errEagerParent}, wantErr: errEagerParent, wantLimit: 1,
		},
		{
			name: "one parent failure", terminal: "one", prepare: unlimited,
			parents: eagerXParents{err: errEagerParent}, wantErr: errEagerParent, wantLimit: 2,
		},
		{
			name: "first load failure", terminal: "first", prepare: unlimited,
			parents: eagerXParents{ids: []int64{1}}, batchError: errEagerParent, wantErr: errEagerParent,
			wantLimit: 1, wantBatch: 1,
		},
		{
			name: "one load failure", terminal: "one", prepare: unlimited,
			parents: eagerXParents{ids: []int64{1}}, batchError: errEagerParent, wantErr: errEagerParent,
			wantLimit: 2, wantBatch: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			batches := 0
			parents := tc.parents
			executor := relationExecutor{
				readExecutor: readExecutor{query: parents.query},
				send: func(_ context.Context, queued *pgx.Batch) pgx.BatchResults {
					batches++

					if tc.batchError != nil {
						return &relationBatch{failure: tc.batchError}
					}

					return eagerXBatch(queued)
				},
			}
			query := tc.prepare(ratel.From(users).With(users.Orders))

			var (
				model relationUser
				err   error
			)

			if tc.terminal == "one" {
				model, err = ratel.New(executor).Run(query).One(t.Context())
			} else {
				model, err = ratel.New(executor).Run(query).First(t.Context())
			}

			if !errors.Is(err, tc.wantErr) || batches != tc.wantBatch {
				t.Fatalf("err=%v batches=%d", err, batches)
			}

			if !strings.HasSuffix(parents.sql, " LIMIT $1") || !reflect.DeepEqual(parents.args, []any{tc.wantLimit}) {
				t.Fatalf("parent statement %q args %v", parents.sql, parents.args)
			}

			if tc.wantErr != nil {
				if model.ID != 0 {
					t.Fatalf("model returned with error: %v", model)
				}

				return
			}

			if orders, failure := model.Orders(); failure != nil || len(orders) != 1 {
				t.Fatalf("orders=%v err=%v", orders, failure)
			}
		})
	}
}

func TestEagerChunksFailures(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()

	for _, tc := range []struct {
		name       string
		parentErr  error
		batchErr   error
		wantBatch  int
		wantTarget error
	}{
		{name: "parent page failure", parentErr: errEagerParent, wantTarget: errEagerParent},
		{name: "load failure", batchErr: errEagerParent, wantBatch: 1, wantTarget: errEagerParent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parents := &eagerXParents{ids: []int64{1}, err: tc.parentErr}
			batches := 0
			executor := relationExecutor{
				readExecutor: readExecutor{query: parents.query},
				send: func(_ context.Context, queued *pgx.Batch) pgx.BatchResults {
					batches++

					if tc.batchErr != nil {
						return &relationBatch{failure: tc.batchErr}
					}

					return eagerXBatch(queued)
				},
			}

			yields := 0

			for page, err := range ratel.New(executor).Run(ratel.From(users).With(users.Orders)).Chunks(t.Context(), 1) {
				yields++

				if page != nil || !errors.Is(err, tc.wantTarget) {
					t.Fatalf("page=%v err=%v", page, err)
				}
			}

			if yields != 1 || batches != tc.wantBatch {
				t.Fatalf("yields=%d batches=%d", yields, batches)
			}
		})
	}
}

func TestEagerChunksStopsEarly(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	parents := &eagerXParents{ids: []int64{1}}
	executor := relationExecutor{
		readExecutor: readExecutor{query: parents.query},
		send:         func(_ context.Context, queued *pgx.Batch) pgx.BatchResults { return eagerXBatch(queued) },
	}

	pages := 0

	// A full page of size 1 would make the iterator read a second page unless the consumer stops.
	for page, err := range ratel.New(executor).Run(ratel.From(users).With(users.Orders)).Chunks(t.Context(), 1) {
		if err != nil || len(page) != 1 {
			t.Fatalf("page=%v err=%v", page, err)
		}

		pages++

		break
	}

	if pages != 1 || parents.calls != 1 {
		t.Fatalf("pages=%d parent queries=%d", pages, parents.calls)
	}
}

func BenchmarkEagerWith(b *testing.B) {
	users, _ := relationDescriptors()
	parents := &eagerXParents{ids: []int64{1}}
	executor := relationExecutor{
		readExecutor: readExecutor{query: parents.query},
		send:         func(_ context.Context, queued *pgx.Batch) pgx.BatchResults { return eagerXBatch(queued) },
	}
	query := ratel.From(users).With(users.Orders).With(users.Orders.Limit(2))
	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEagerWithCount(b *testing.B) {
	users, _ := relationDescriptors()
	parents := &eagerXParents{ids: []int64{1}}
	executor := relationExecutor{
		readExecutor: readExecutor{query: parents.query},
		send:         func(_ context.Context, queued *pgx.Batch) pgx.BatchResults { return eagerXBatch(queued) },
	}
	query := ratel.From(users).WithCount(users.Orders).WithCount(users.Orders)
	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(query).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEagerSelect(b *testing.B) {
	users, _ := relationDescriptors()
	executor := relationExecutor{
		readExecutor: readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return &readRows{ids: []int64{1}}, nil
		}},
		send: func(context.Context, *pgx.Batch) pgx.BatchResults {
			return &relationBatch{rows: []*relationRows{{values: [][]any{{1, int64(11), int64(1)}}}}}
		},
	}
	database := ratel.New(executor)
	query := ratel.From(users).With(users.Orders)

	built := query.Where(users.ID.Eq(1)).OrderBy(users.ID.Asc()).Limit(1).Offset(0)

	for _, terminal := range []string{"list", "one", "first", "chunks"} {
		b.Run(terminal, func(b *testing.B) {
			target := query
			if terminal != "list" {
				target = built
			}

			b.ReportAllocs()

			for b.Loop() {
				if _, err := eagerTerminal(b.Context(), database, target, terminal); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
