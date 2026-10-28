package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/trace/noop"

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
	query := database.From(users).With(users.Orders).
		Where(users.ID.Eq(1)).OrderBy(users.ID.Asc()).Limit(1).Offset(0)

	if terminal == "count" {
		query = database.From(users).WithCount(users.Orders)
	}

	models, err := eagerTerminal(t.Context(), query, terminal)
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
	ctx context.Context, query ratel.EagerSelect[relationUser], terminal string,
) ([]relationUser, error) {
	switch terminal {
	case "one":
		model, err := query.One(ctx)

		return []relationUser{model}, err
	case "first":
		model, err := query.First(ctx)

		return []relationUser{model}, err
	case "chunks":
		for page, err := range query.Chunks(ctx, 1) {
			return page, err
		}

		return nil, nil
	default:
		return query.List(ctx)
	}
}

func TestEagerNoAll(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()

	query := ratel.New(readExecutor{}).From(users).With(users.Orders)
	if _, exists := reflect.TypeOf(query).MethodByName("All"); exists {
		t.Fatal("With exposes All")
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

	result, err := ratel.New(executor).From(users).With(users.Orders).List(t.Context())
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
			query := database.From(users).With(ratel.Rel[relationUser, relationOrder]{})

			if terminal == "nil count" {
				query = database.From(users).WithCount(nil)
			}

			_, err := eagerTerminal(t.Context(), query, terminal)
			if !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatal(err)
			}
		})
	}
}

func TestEagerTelemetry(t *testing.T) {
	t.Parallel()

	for _, terminal := range []string{"list", "one", "first", "chunks"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()

			var spans []*observedSpan
			provider := observedProvider{TracerProvider: noop.NewTracerProvider(), spans: &spans}
			executor := relationExecutor{
				readExecutor: readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
					return &readRows{ids: []int64{1}}, nil
				}},
				send: func(context.Context, *pgx.Batch) pgx.BatchResults {
					depth := spans[len(spans)-1]
					if depth.Span != spans[0] {
						t.Fatal("depth is not a child of the eager operation")
					}

					return &relationBatch{rows: []*relationRows{{}}}
				},
			}
			users, _ := relationDescriptors()

			query := ratel.New(executor, ratel.WithTracerProvider(provider)).From(users).With(users.Orders).Limit(1)
			if _, err := eagerTerminal(t.Context(), query, terminal); err != nil {
				t.Fatal(err)
			}

			for _, span := range spans {
				if !span.ended {
					t.Fatal("span did not end")
				}
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
			query := database.From(users).With(users.Orders)

			var err error

			switch terminal {
			case "load":
				err = database.Load(t.Context(), []relationUser{{ID: 1}}, users.Orders)
			case "override":
				_, err = ratel.New(executor).From(users).With(users.Orders).
					List(t.Context(), ratel.ExecMode(pgx.QueryExecModeExec))
			default:
				_, err = eagerTerminal(t.Context(), query, terminal)
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

		query := database.From(users).With(relation).WithCount(users.Orders)
		if _, err := query.List(b.Context()); err != nil {
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
	query := ratel.New(executor).From(users).With(users.Orders)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := query.List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
