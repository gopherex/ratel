package ratel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestRelationSQLStrategies(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		configure func(ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser]
		fragments []string
		invalid   bool
	}{
		{
			name: "batch", fragments: []string{"ANY($1)", "WITH ORDINALITY"},
			configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] { return rel },
		},
		{name: "lateral", configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] {
			return rel.Limit(2).Offset(1)
		}, fragments: []string{"CROSS JOIN LATERAL", "LIMIT $2 OFFSET $3"}},
		{name: "window", configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] {
			return rel.Limit(2).Strategy(ratel.Window())
		}, fragments: []string{"row_number() OVER", "PARTITION BY", "\"__ratel_rank\" -"}},
		{
			name: "count", fragments: []string{"count(*)", "GROUP BY"},
			configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] { return rel.Count() },
		},
		{name: "correlated", configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] {
			return rel.Strategy(ratel.Correlated()).Count()
		}, fragments: []string{"(SELECT count(*)", "unnest("}},
		{name: "joined many", configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] {
			return rel.Strategy(ratel.Joined())
		}, invalid: true},
		{name: "correlated rows", configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] {
			return rel.Strategy(ratel.Correlated())
		}, invalid: true},
		{
			name: "negative", invalid: true,
			configure: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Relation[relationUser] { return rel.Limit(-1) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			calls := 0
			executor := relationExecutor{send: func(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
				calls++
				for _, fragment := range tc.fragments {
					if !strings.Contains(batch.QueuedQueries[0].SQL, fragment) {
						t.Fatalf("missing %q: %s", fragment, batch.QueuedQueries[0].SQL)
					}
				}

				return &relationBatch{rows: []*relationRows{{}}}
			}}

			err := ratel.New(executor).Load(t.Context(), []relationUser{{ID: 1}}, tc.configure(users.Orders))
			if tc.invalid {
				if !errors.Is(err, ratel.ErrInvalidQuery) || calls != 0 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRelationInvalidMetadata(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"zero", "schema", "primary key", "key column", "type", "projection",
		"cardinality", "attach", "through", "through keys", "count",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, orders := relationDescriptors()
			keys := []ratel.RelKey[relationUser]{{
				Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"},
				Values: func(models []relationUser) any { return make([]int64, len(models)) },
			}}
			cardinality := ratel.RelMany
			attach := func(model *relationUser, children []relationOrder) { model.R.Orders = ratel.LoadedFrom(children) }
			options := []ratel.RelOption[relationUser, relationOrder]{}

			switch name {
			case "schema":
				orders = orders.InSchema("")
			case "primary key":
				orders = ratel.NewTable("app", "orders", []string{"id"},
					func(pgx.Row) (relationOrder, error) { return relationOrder{}, nil })
			case "key column":
				keys[0].Column = "missing"
			case "type":
				keys[0].Type = pgx.Identifier{"int8"}
			case "projection":
				keys[0].Values = nil
			case "cardinality":
				cardinality = 0
			case "attach":
				attach = nil
			case "through":
				options = append(options, ratel.Through[relationUser, relationOrder]("", "links", "id"))
			case "through keys":
				options = append(options, ratel.Through[relationUser, relationOrder]("app", "links"))
			}

			relation := ratel.NewRel(orders, cardinality, keys, attach, options...)
			if name == "zero" {
				relation = ratel.Rel[relationUser, relationOrder]{}
			}

			var requested ratel.Relation[relationUser] = relation
			if name == "count" {
				requested = relation.Count()
			}

			executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
				t.Fatal("invalid metadata reached executor")

				return nil
			}}

			err := ratel.New(executor).Load(t.Context(), []relationUser{{ID: 1}}, requested)
			if !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkRelationStrategies(b *testing.B) {
	users, _ := relationDescriptors()
	for _, tc := range []struct {
		name string
		rel  ratel.Relation[relationUser]
	}{
		{"batch", users.Orders},
		{"lateral", users.Orders.Limit(5)},
		{"window", users.Orders.Limit(5).Strategy(ratel.Window())},
		{"count", users.Orders.Count()},
		{"correlated", users.Orders.Strategy(ratel.Correlated()).Count()},
	} {
		b.Run(tc.name, func(b *testing.B) {
			executor := relationExecutor{send: func(context.Context, *pgx.Batch) pgx.BatchResults {
				return &relationBatch{rows: []*relationRows{{}}}
			}}
			database := ratel.New(executor)
			models := []relationUser{{ID: 1}}

			b.ReportAllocs()

			for b.Loop() {
				if err := database.Load(b.Context(), models, tc.rel); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
