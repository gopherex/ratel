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
		"zero", "schema", "primary key", "key column", "type", "base", "projection",
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
			case "base":
				keys[0].Base = pgx.Identifier{"text"}
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

func relInt64Keys(columns ...string) []ratel.RelKey[relationOrder] {
	keys := make([]ratel.RelKey[relationOrder], len(columns))
	for index, column := range columns {
		keys[index] = ratel.RelKey[relationOrder]{
			Column: column, Parent: column, Type: pgx.Identifier{"pg_catalog", "int8"},
			Values: func(models []relationOrder) any { return make([]int64, len(models)) },
		}
	}

	return keys
}

func relTable(columns, primaryKey []string) ratel.Table[relationOrder] {
	return ratel.NewTable("app", "orders", columns,
		func(pgx.Row) (relationOrder, error) { return relationOrder{}, nil },
		ratel.PrimaryKey(primaryKey, func(model relationOrder) []any { return []any{model.ID} }))
}

func TestRelationSQLShapes(t *testing.T) {
	t.Parallel()

	byID := ratel.NewCol[relationOrder, int64]("id")
	owner := ratel.NewCol[relationOrder, int64]("user_id")
	attach := func(*relationOrder, []relationOrder) {}
	plain := []string{"id", "user_id"}

	for _, tc := range []struct {
		name      string
		relation  ratel.Relation[relationOrder]
		fragments []string
		absent    []string
		args      int
	}{
		{
			name: "lateral ordered",
			relation: ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany, relInt64Keys("user_id"), attach).
				Limit(2).Offset(1).OrderBy(byID.Asc(), owner.Desc()),
			fragments: []string{
				`WHERE "q"."user_id" = "p"."k0" ORDER BY "q"."id" ASC, "q"."user_id" DESC LIMIT $2 OFFSET $3) AS "c"`,
				`AS "c" ORDER BY "c"."id" ASC, "c"."user_id" DESC`,
			},
			absent: []string{`ORDER BY "p"`},
			args:   3,
		},
		{
			name: "base type cast to the key type",
			relation: ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany, []ratel.RelKey[relationOrder]{{
				Column: "user_id", Parent: "user_id", Type: pgx.Identifier{"app", "status"},
				Base:   pgx.Identifier{"pg_catalog", "text"},
				Values: func(models []relationOrder) any { return make([]string, len(models)) },
			}}, attach),
			fragments: []string{`unnest($1::"pg_catalog"."text"[]::"app"."status"[]) WITH ORDINALITY`},
			args:      1,
		},
		{
			name:     "lateral unordered",
			relation: ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany, relInt64Keys("user_id"), attach).Limit(2),
			fragments: []string{
				`WHERE "q"."user_id" = "p"."k0" LIMIT $2) AS "c"`,
			},
			absent: []string{"ASC", "DESC", "ORDER BY"},
			args:   2,
		},
		{
			name: "window ordered",
			relation: ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany, relInt64Keys("user_id"), attach).
				Limit(3).OrderBy(byID.Desc()).Strategy(ratel.Window()),
			fragments: []string{
				`row_number() OVER (PARTITION BY "p"."__ratel_parent" ORDER BY "q"."id" DESC) AS "__ratel_rank"`,
				`WHERE "w"."__ratel_rank" > $2 AND "w"."__ratel_rank" - $3 <= $4 ORDER BY "w"."__ratel_rank"`,
			},
			args: 4,
		},
		{
			name: "window avoids column collisions",
			relation: ratel.NewRel(
				relTable([]string{"id", "user_id", "__ratel_parent", "__ratel_rank"}, []string{"id"}),
				ratel.RelMany, relInt64Keys("user_id"), attach).Strategy(ratel.Window()),
			fragments: []string{
				`SELECT "w"."__ratel_parent_", `,
				`"p"."__ratel_parent" AS "__ratel_parent_"`,
				`AS "__ratel_rank_" FROM`,
			},
			absent: []string{`ORDER BY "w"`},
			args:   2,
		},
		{
			name: "window avoids repeated collisions",
			relation: ratel.NewRel(
				relTable([]string{"id", "user_id", "__ratel_parent", "__ratel_parent_"}, []string{"id"}),
				ratel.RelMany, relInt64Keys("user_id"), attach).Strategy(ratel.Window()),
			fragments: []string{`"p"."__ratel_parent" AS "__ratel_parent__"`},
			args:      2,
		},
		{
			name: "composite keys",
			relation: ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany,
				relInt64Keys("user_id", "id"), attach),
			fragments: []string{
				`unnest($1::"pg_catalog"."int8"[], $2::"pg_catalog"."int8"[]) ` +
					`WITH ORDINALITY AS "p" ("k0", "k1", "__ratel_parent")`,
				`ON "q"."user_id" = "p"."k0" AND "q"."id" = "p"."k1" WHERE TRUE`,
			},
			absent: []string{"ANY($1)"},
			args:   2,
		},
		{
			name: "composite keys count",
			relation: ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany,
				relInt64Keys("user_id", "id"), attach,
				ratel.RelCount[relationOrder, relationOrder](func(*relationOrder, int64) {})).Count(),
			fragments: []string{`GROUP BY "p"."__ratel_parent", "q"."user_id", "q"."id"`},
			args:      2,
		},
		{
			name: "through composite target keys",
			relation: ratel.NewRel(relTable(plain, []string{"id", "user_id"}), ratel.RelMany,
				relInt64Keys("owner_id"), attach,
				ratel.Through[relationOrder, relationOrder]("app", "links", "order_id", "order_user")),
			fragments: []string{
				`JOIN ("app"."orders" AS "q" JOIN "app"."links" AS "l" ON ` +
					`"l"."order_id" = "q"."id" AND "l"."order_user" = "q"."user_id") ON "l"."owner_id" = "p"."k0"`,
			},
			args: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queued, err := relCapture(t, tc.relation)
			statement := queued.SQL

			if err != nil || len(queued.Arguments) != tc.args {
				t.Fatalf("args=%d err=%v\n%s", len(queued.Arguments), err, statement)
			}

			for _, fragment := range tc.fragments {
				if !strings.Contains(statement, fragment) {
					t.Fatalf("missing %q:\n%s", fragment, statement)
				}
			}

			for _, fragment := range tc.absent {
				if strings.Contains(statement, fragment) {
					t.Fatalf("unexpected %q:\n%s", fragment, statement)
				}
			}
		})
	}
}

func TestRelationInvalidShapes(t *testing.T) {
	t.Parallel()

	attach := func(*relationOrder, []relationOrder) {}
	plain := []string{"id", "user_id"}
	badType := relInt64Keys("user_id")
	badType[0].Type = pgx.Identifier{"pg_catalog", ""}

	for _, tc := range []struct {
		name     string
		relation ratel.Relation[relationOrder]
	}{
		{"primary key column missing", ratel.NewRel(relTable(plain, []string{"missing"}), ratel.RelMany,
			relInt64Keys("user_id"), attach)},
		{"no keys", ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany, nil, attach)},
		{"empty type part", ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany, badType, attach)},
		{"invalid order column", ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany,
			relInt64Keys("user_id"), attach).Limit(1).OrderBy(ratel.NewCol[relationOrder, int64]("").Asc())},
		{"window invalid order column", ratel.NewRel(relTable(plain, []string{"id"}), ratel.RelMany,
			relInt64Keys("user_id"), attach).OrderBy(ratel.NewCol[relationOrder, int64]("").Asc()).
			Strategy(ratel.Window())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queued, err := relCapture(t, tc.relation)
			if !errors.Is(err, ratel.ErrInvalidQuery) || queued.SQL != "" {
				t.Fatalf("statement=%q err=%v", queued.SQL, err)
			}
		})
	}
}

func BenchmarkRelationShapes(b *testing.B) {
	users, orders := relationDescriptors()
	benchKeys := []ratel.RelKey[relationUser]{{
		Column: "user_id", Parent: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func(models []relationUser) any {
			values := make([]int64, len(models))
			for index, model := range models {
				values[index] = model.ID
			}

			return values
		},
	}}
	attach := func(model *relationUser, children []relationOrder) { model.R.Orders = ratel.LoadedFrom(children) }
	count := ratel.RelCount[relationUser, relationOrder](func(model *relationUser, total int64) {
		model.R.OrderCount = ratel.LoadedFrom(total)
	})
	through := ratel.NewRel(orders, ratel.RelMany, benchKeys, attach, count,
		ratel.Through[relationUser, relationOrder]("app", "user_orders", "order_id"))
	joined := ratel.NewRel(orders, ratel.RelOne, benchKeys, attach).Strategy(ratel.Joined())
	nested := ratel.NewRel(relTable([]string{"id", "user_id"}, []string{"id"}), ratel.RelMany,
		relInt64Keys("user_id"), func(*relationOrder, []relationOrder) {},
		ratel.RelCount[relationOrder, relationOrder](func(*relationOrder, int64) {}))

	for _, tc := range []struct {
		name string
		rel  ratel.Relation[relationUser]
	}{
		{"through", through},
		{"through count", through.Count()},
		{"through limit", through.Limit(2)},
		{"joined", joined},
		{"nested count", users.Orders.WithCount(nested)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			database := ratel.New(relationExecutor{
				send: func(_ context.Context, queued *pgx.Batch) pgx.BatchResults { return eagerXBatch(queued) },
			})
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

func BenchmarkRelationStrategies(b *testing.B) {
	users, _ := relationDescriptors()
	byID := ratel.NewCol[relationOrder, int64]("id")

	for _, tc := range []struct {
		name string
		rel  ratel.Relation[relationUser]
	}{
		{"batch", users.Orders},
		{"batch ordered", users.Orders.OrderBy(byID.Desc())},
		{"lateral ordered", users.Orders.Limit(5).OrderBy(byID.Desc())},
		{"window ordered", users.Orders.Limit(5).OrderBy(byID.Desc()).Strategy(ratel.Window())},
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
