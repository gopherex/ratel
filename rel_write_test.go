package ratel_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func linkRelation(base pgx.Identifier) ratel.Rel[relationUser, relationOrder] {
	_, orders := relationDescriptors()

	return ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationUser]{{
		Column: "user_id", Parent: "id", Type: pgx.Identifier{"pg_catalog", "int8"}, Base: base,
		Values: func(models []relationUser) any {
			out := make([]int64, len(models))
			for index := range models {
				out[index] = models[index].ID
			}

			return out
		},
	}}, func(*relationUser, []relationOrder) {},
		ratel.Through[relationUser, relationOrder]("app", "user_orders", "order_id"))
}

func TestLinkWrite(t *testing.T) {
	t.Parallel()

	links := linkRelation(nil)
	user := relationUser{ID: 7}
	first, second := relationOrder{ID: 1}, relationOrder{ID: 2}

	const parent = `("user_id") = (($1::"pg_catalog"."int8"[])[1])`

	for _, tc := range []struct {
		name  string
		write ratel.LinkWrite
		sql   string
		args  []any
	}{
		{
			name:  "attach",
			write: links.Attach(user, first, second),
			sql: `INSERT INTO "app"."user_orders" ("user_id", "order_id") VALUES ` +
				`(($1::"pg_catalog"."int8"[])[1], $2), (($1::"pg_catalog"."int8"[])[1], $3) ON CONFLICT DO NOTHING`,
			args: []any{[]int64{7}, int64(1), int64(2)},
		},
		{
			name:  "detach",
			write: links.Detach(user, first),
			sql:   `DELETE FROM "app"."user_orders" WHERE ` + parent + ` AND ("order_id") IN (($2))`,
			args:  []any{[]int64{7}, int64(1)},
		},
		{
			name:  "sync",
			write: links.Sync(user, first),
			sql: `WITH "removed" AS (DELETE FROM "app"."user_orders" WHERE ` + parent +
				` AND ("order_id") NOT IN (($2))) INSERT INTO "app"."user_orders" ("user_id", "order_id") VALUES ` +
				`(($1::"pg_catalog"."int8"[])[1], $3) ON CONFLICT DO NOTHING`,
			args: []any{[]int64{7}, int64(1), int64(1)},
		},
		{
			name:  "sync to nothing",
			write: links.Sync(user),
			sql:   `DELETE FROM "app"."user_orders" WHERE ` + parent,
			args:  []any{[]int64{7}},
		},
		{
			name:  "base type",
			write: linkRelation(pgx.Identifier{"pg_catalog", "text"}).Detach(user, first),
			sql: `DELETE FROM "app"."user_orders" WHERE ("user_id") = ` +
				`(($1::"pg_catalog"."text"[]::"pg_catalog"."int8"[])[1]) AND ("order_id") IN (($2))`,
			args: []any{[]int64{7}, int64(1)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sql, args, err := tc.write.Build()
			if err != nil || sql != tc.sql || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("Build() = %s, %#v, %v", sql, args, err)
			}
		})
	}
}

func TestLinkWriteInvalid(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	links := linkRelation(nil)
	user := relationUser{ID: 7}

	for _, tc := range []struct {
		name  string
		write ratel.LinkWrite
	}{
		{"attach nothing", links.Attach(user)},
		{"detach nothing", links.Detach(user)},
		{"one-to-many", users.Orders.Attach(user, relationOrder{ID: 1})},
		{"zero relation", ratel.Rel[relationUser, relationOrder]{}.Sync(user)},
		{"zero value", ratel.LinkWrite{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := tc.write.Build(); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("Build() error = %v", err)
			}

			if _, err := ratel.New(relationExecutor{}).Run(tc.write).Exec(t.Context()); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("Exec() error = %v", err)
			}
		})
	}
}

func BenchmarkLinkWrite(b *testing.B) {
	links := linkRelation(nil)
	user := relationUser{ID: 7}
	children := []relationOrder{{ID: 1}, {ID: 2}, {ID: 3}}

	for b.Loop() {
		if _, _, err := links.Sync(user, children...).Build(); err != nil {
			b.Fatal(err)
		}
	}
}
