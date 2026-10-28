package ratel_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestHas(t *testing.T) {
	t.Parallel()

	users, orders := relationDescriptors()
	orderID := ratel.NewCol[relationOrder, int64]("id")
	database := ratel.New(&pgx.Conn{})

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
			"exists", database.From(users).Where(users.Orders.Has(orderID.Gt(5))),
			where + `EXISTS (SELECT 1 FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND "h0"."id" > $1)`,
			[]any{int64(5)},
		},
		{
			"relation where", database.From(users).Where(users.Orders.Where(orderID.Lt(9)).Has(orderID.Gt(5))),
			where + `EXISTS (SELECT 1 FROM "app"."orders" AS "h0" WHERE "h0"."user_id" = "q"."id" AND ` +
				`("h0"."id" < $1 AND "h0"."id" > $2))`,
			[]any{int64(9), int64(5)},
		},
		{
			"through", database.From(users).Where(join.Has(ratel.Cond[relationOrder]{})),
			where + `EXISTS (SELECT 1 FROM "app"."orders" AS "h0" JOIN "app"."user_orders" AS "h0l" ON ` +
				`"h0l"."order_id" = "h0"."id" WHERE "h0l"."user_id" = "q"."id" AND TRUE)`,
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := tc.query.SQL()
			if err != nil || statement.SQL != tc.sql || !reflect.DeepEqual(statement.Args, tc.args) {
				t.Fatalf("got %+v, %v; want %s, %#v", statement, err, tc.sql, tc.args)
			}
		})
	}

	unkeyed := ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationUser]{{
		Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"},
		Values: func([]relationUser) any { return []int64{} },
	}}, func(*relationUser, []relationOrder) {})

	if _, err := database.From(users).Where(unkeyed.Has(orderID.Gt(1))).SQL(); !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Has without a parent column = %v", err)
	}
}

func BenchmarkHas(b *testing.B) {
	users, _ := relationDescriptors()
	orderID := ratel.NewCol[relationOrder, int64]("id")
	query := ratel.New(&pgx.Conn{}).From(users).Where(users.Orders.Has(orderID.Gt(5)))

	for b.Loop() {
		if _, err := query.SQL(); err != nil {
			b.Fatal(err)
		}
	}
}
