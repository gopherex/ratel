package ratel_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestHas(t *testing.T) {
	t.Parallel()

	users, orders := relationDescriptors()
	orderID := ratel.NewCol[relationOrder, int64]("id")

	join := ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationUser]{{
		Column: "user_id", Parent: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func([]relationUser) any { return []int64{} },
	}}, func(*relationUser, []relationOrder) {},
		ratel.Through[relationUser, relationOrder]("app", "user_orders", "order_id"))

	const where = `SELECT "q"."id" FROM "app"."users" AS "q" WHERE `

	for _, tc := range []struct {
		name  string
		query ratel.Select[relationUser]
		sql   string
		args  []any
	}{
		{
			"exists", ratel.From(users).Where(users.Orders.Has(orderID.Gt(5))),
			where + `EXISTS (SELECT 1 FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND "h0"."id" > $1)`,
			[]any{int64(5)},
		},
		{
			"relation where", ratel.From(users).Where(users.Orders.Where(orderID.Lt(9)).Has(orderID.Gt(5))),
			where + `EXISTS (SELECT 1 FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND ` +
				`("h0"."id" < $1 AND "h0"."id" > $2))`,
			[]any{int64(9), int64(5)},
		},
		{
			"none", ratel.From(users).Where(users.Orders.HasNone(orderID.Gt(5))),
			where + `NOT (EXISTS (SELECT 1 FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND "h0"."id" > $1))`,
			[]any{int64(5)},
		},
		{
			"every", ratel.From(users).Where(users.Orders.Every(orderID.Gt(5))),
			where + `NOT (EXISTS (SELECT 1 FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND ` +
				`NOT ("h0"."id" > $1)))`,
			[]any{int64(5)},
		},
		{
			"count", ratel.From(users).Where(users.Orders.Where(orderID.Lt(9)).HasCount().Gte(2)),
			where + `(SELECT count(*) FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND ` +
				`("h0"."id" < $1 AND TRUE)) >= $2`,
			[]any{int64(9), int64(2)},
		},
		{
			"count through", ratel.From(users).Where(join.HasCount().Eq(0)),
			where + `(SELECT count(*) FROM "app"."orders" AS "h0" JOIN "app"."user_orders" AS "h0l" ON ` +
				`"h0l"."order_id" = "h0"."id" WHERE "h0l"."user_id" = "q"."id" AND TRUE) = $1`,
			[]any{int64(0)},
		},
		{
			"through", ratel.From(users).Where(join.Has(ratel.Cond[relationOrder]{})),
			where + `EXISTS (SELECT 1 FROM "app"."orders" AS "h0" JOIN "app"."user_orders" AS "h0l" ON ` +
				`"h0l"."order_id" = "h0"."id" WHERE "h0l"."user_id" = "q"."id" AND TRUE)`,
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := build(tc.query)
			if err != nil || statement.SQL != tc.sql || !reflect.DeepEqual(statement.Args, tc.args) {
				t.Fatalf("got %+v, %v; want %s, %#v", statement, err, tc.sql, tc.args)
			}
		})
	}

	unkeyed := ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationUser]{{
		Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func([]relationUser) any { return []int64{} },
	}}, func(*relationUser, []relationOrder) {})

	if _, err := build(ratel.From(users).Where(unkeyed.Has(orderID.Gt(1)))); !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Has without a parent column = %v", err)
	}
}

func TestHasCountOperators(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	count := users.Orders.HasCount()

	for _, tc := range []struct {
		operator  string
		condition ratel.Cond[relationUser]
	}{
		{"=", count.Eq(1)},
		{"<>", count.Ne(1)},
		{"<", count.Lt(1)},
		{"<=", count.Lte(1)},
		{">", count.Gt(1)},
		{">=", count.Gte(1)},
	} {
		t.Run(tc.operator, func(t *testing.T) {
			t.Parallel()

			statement, err := build(ratel.From(users).Where(tc.condition))
			if err != nil || !strings.HasSuffix(statement.SQL, ") "+tc.operator+" $1") {
				t.Fatalf("got %+v, %v", statement, err)
			}
		})
	}
}

func TestBwHasCompositeThrough(t *testing.T) {
	t.Parallel()

	users := ratel.NewTable("app", "users", []string{"id", "email", "manager_id"}, scanReadUser,
		ratel.PrimaryKey([]string{"id", "email"}, func(user readUser) []any { return []any{user.ID, user.Email} }))
	friends := ratel.NewRel(users, ratel.RelMany, []ratel.RelKey[readUser]{{
		Column: "user_id", Parent: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func([]readUser) any { return []int64{} },
	}}, func(*readUser, []readUser) {}, ratel.Through[readUser, readUser]("app", "friends", "friend_id", "friend_email"))
	id := ratel.NewCol[readUser, int64]("id")

	statement, err := build(ratel.From(users).Where(friends.Has(id.Gt(5))))
	if err != nil {
		t.Fatal(err)
	}

	const exists = `WHERE EXISTS (SELECT 1 FROM "app"."users" AS "h0" JOIN "app"."friends" AS "h0l" ON ` +
		`"h0l"."friend_id" = "h0"."id" AND "h0l"."friend_email" = "h0"."email" ` +
		`WHERE "h0l"."user_id" = "q"."id" AND "h0"."id" > $1)`

	if !strings.HasSuffix(statement.SQL, exists) || !reflect.DeepEqual(statement.Args, []any{int64(5)}) {
		t.Fatalf("got %+v", statement)
	}
}

func BenchmarkHas(b *testing.B) {
	users, orders := relationDescriptors()
	orderID := ratel.NewCol[relationOrder, int64]("id")
	join := ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationUser]{{
		Column: "user_id", Parent: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func([]relationUser) any { return []int64{} },
	}}, func(*relationUser, []relationOrder) {},
		ratel.Through[relationUser, relationOrder]("app", "user_orders", "order_id"))

	cases := []struct {
		name  string
		query ratel.Select[relationUser]
	}{
		{"direct", ratel.From(users).Where(users.Orders.Has(orderID.Gt(5)))},
		{"through", ratel.From(users).Where(join.Has(orderID.Gt(5)))},
		{"count", ratel.From(users).Where(users.Orders.HasCount().Gt(3))},
	}

	for index := range cases {
		tc := &cases[index]

		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := build(tc.query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
