package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

const usersSQL = `SELECT "q"."id", "q"."email", "q"."manager_id" FROM "app"."users" AS "q"`

func TestSelectClauses(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	base := ratel.New(&pgx.Conn{}).From(users)

	for _, tc := range []struct {
		name  string
		query ratel.Select[readUser]
		sql   string
		args  []any
	}{
		{
			"after ascending", base.OrderBy(users.ID.Asc()).After(int64(5)),
			usersSQL + ` WHERE (("q"."id" > $1)) ORDER BY "q"."id" ASC`,
			[]any{int64(5)},
		},
		{
			"after mixed", base.Where(users.ID.Gt(0)).OrderBy(users.Email.Desc(), users.ID.Asc()).After("m", int64(5)),
			usersSQL + ` WHERE ("q"."id" > $1 AND (("q"."email" < $2) OR ("q"."email" = $3 AND "q"."id" > $4)))` +
				` ORDER BY "q"."email" DESC, "q"."id" ASC`,
			[]any{int64(0), "m", "m", int64(5)},
		},
		{"for update", base.ForUpdate(), usersSQL + ` FOR UPDATE`, nil},
		{"skip locked", base.ForUpdate().SkipLocked().Limit(1), usersSQL + ` LIMIT $1 FOR UPDATE SKIP LOCKED`, []any{1}},
		{"share nowait", base.ForShare().NoWait(), usersSQL + ` FOR SHARE NOWAIT`, nil},
		{
			"raw", base.Where(ratel.Raw[readUser](`"q"."email" ~ $2 AND '$1' <> $1 -- $3`+"\n", int64(1), "x")),
			usersSQL + ` WHERE ("q"."email" ~ $1 AND '$1' <> $2 -- $3` + "\n)",
			[]any{"x", int64(1)},
		},
		{
			"raw dollar quoted", base.Where(ratel.Raw[readUser](`$q$ $1 $q$ = "$1" OR E'\'$1' = $1`, "a")),
			usersSQL + ` WHERE ($q$ $1 $q$ = "$1" OR E'\'$1' = $1)`,
			[]any{"a"},
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
}

func TestJoin(t *testing.T) {
	t.Parallel()

	users, orders := relationDescriptors()
	userID := ratel.NewCol[relationOrder, int64]("user_id")
	orderID := ratel.NewCol[relationOrder, int64]("id")
	database := ratel.New(&pgx.Conn{})

	query := database.From(users).Join(orders, users.ID.EqCol(userID)).Where(users.ID.Gt(1)).
		Join(database.From(orders).Where(orderID.Gt(9)), users.ID.EqCol(userID))

	statement, err := query.SQL()
	want := `SELECT "q"."id" FROM "app"."users" AS "q" JOIN "app"."orders" AS "j0" ON "q"."id" = "j0"."user_id"` +
		` JOIN (SELECT "q"."id", "q"."user_id" FROM "app"."orders" AS "q" WHERE "q"."id" > $1) AS "j1"` +
		` ON "q"."id" = "j1"."user_id" WHERE "q"."id" > $2`

	if err != nil || statement.SQL != want || !reflect.DeepEqual(statement.Args, []any{int64(9), int64(1)}) {
		t.Fatalf("got %+v, %v; want %s", statement, err, want)
	}
}

func TestSelectClausesInvalid(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	base := ratel.New(&pgx.Conn{}).From(users)

	for _, tc := range []struct {
		name  string
		query ratel.Select[readUser]
	}{
		{"wait without lock", base.SkipLocked()},
		{"after without order", base.After(int64(1))},
		{"after arity", base.OrderBy(users.ID.Asc()).After(int64(1), int64(2))},
		{"raw marker without argument", base.Where(ratel.Raw[readUser](`"q"."id" = $2`, int64(1)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := tc.query.SQL(); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("SQL() error = %v", err)
			}
		})
	}
}

type scalarRow struct{ value any }

func (r scalarRow) Scan(dest ...any) error {
	switch target := dest[0].(type) {
	case *int64:
		*target = r.value.(int64)
	case *bool:
		*target = r.value.(bool)
	}

	return nil
}

type scalarExecutor struct {
	ratel.Executor
	sql   *string
	value any
}

func (e scalarExecutor) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	*e.sql = sql

	return scalarRow{value: e.value}
}

func TestCountExists(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	var sql string

	count, err := ratel.New(scalarExecutor{sql: &sql, value: int64(7)}).From(users).Limit(10).Count(t.Context())
	if err != nil || count != 7 || sql != `SELECT count(*) FROM (`+usersSQL+` LIMIT $1) AS "c"` {
		t.Fatalf("Count() = %d, %v; SQL %s", count, err, sql)
	}

	exists, err := ratel.New(scalarExecutor{sql: &sql, value: true}).From(users).Where(users.ID.Eq(1)).Exists(t.Context())
	if err != nil || !exists || sql != `SELECT EXISTS (`+usersSQL+` WHERE "q"."id" = $1)` {
		t.Fatalf("Exists() = %v, %v; SQL %s", exists, err, sql)
	}
}

func TestPluck(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	rows := &readRows{ids: []int64{3, 4}}

	var sql string

	executor := readExecutor{query: func(_ context.Context, got string, _ ...any) (pgx.Rows, error) {
		sql = got

		return rows, nil
	}}

	ids, err := ratel.New(executor).From(users).OrderBy(users.ID.Asc()).Pluck(users.ID).List(t.Context())
	if err != nil || !reflect.DeepEqual(ids, []int64{3, 4}) ||
		sql != `SELECT "p"."id" FROM (`+usersSQL+` ORDER BY "q"."id" ASC) AS "p"` || !rows.closed {
		t.Fatalf("Pluck() = %v, %v; SQL %s", ids, err, sql)
	}
}

func BenchmarkKeyset(b *testing.B) {
	users := userDescriptor()
	query := ratel.New(&pgx.Conn{}).From(users).OrderBy(users.Email.Desc(), users.ID.Asc()).After("m", int64(5))

	for b.Loop() {
		if _, err := query.SQL(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRaw(b *testing.B) {
	users := userDescriptor()
	query := ratel.New(&pgx.Conn{}).From(users).Where(ratel.Raw[readUser](`"q"."email" ~ $1 AND '$1' <> $2`, "a", "b"))

	for b.Loop() {
		if _, err := query.SQL(); err != nil {
			b.Fatal(err)
		}
	}
}
