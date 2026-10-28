package ratel_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

type readUser struct {
	ID        int64
	Email     string
	ManagerID ratel.Null[int64]
}

type readUsers struct {
	ratel.Table[readUser]
	ID        ratel.Col[readUser, int64]
	Email     ratel.Col[readUser, string]
	ManagerID ratel.NullCol[readUser, int64]
}

func userDescriptor() readUsers {
	return readUsers{
		Table: ratel.NewTable("app", "users", []string{"id", "email", "manager_id"}, scanReadUser,
			ratel.PrimaryKey([]string{"id"}, func(user readUser) []any { return []any{user.ID} })),
		ID:        ratel.NewCol[readUser, int64]("id"),
		Email:     ratel.NewCol[readUser, string]("email"),
		ManagerID: ratel.NewNullCol[readUser, int64]("manager_id"),
	}
}

func scanReadUser(row pgx.Row) (readUser, error) {
	var user readUser

	err := row.Scan(&user.ID, &user.Email, &user.ManagerID)

	return user, err
}

func TestSelectSQL(t *testing.T) {
	t.Parallel()

	const projection = `SELECT "q"."id", "q"."email", "q"."manager_id" FROM `

	const table = projection + `"app"."users" AS "q"`

	users := userDescriptor()
	database := ratel.New(&pgx.Conn{})
	base := database.From(users)

	for _, tc := range []struct {
		name  string
		query ratel.Select[readUser]
		sql   string
		args  []any
	}{
		{"table", base, table, nil},
		{
			"clauses", base.Where(users.ID.Gte(10).And(users.Email.Like("%@example.org").Or(users.ManagerID.IsNull()))).
				OrderBy(users.ID.Desc(), users.Email.Asc()).Limit(20).Offset(2),
			table + ` WHERE ("q"."id" >= $1 AND ("q"."email" LIKE $2 OR "q"."manager_id" IS NULL))` +
				` ORDER BY "q"."id" DESC, "q"."email" ASC LIMIT $3 OFFSET $4`,
			[]any{int64(10), "%@example.org", 20, 2},
		},
		{"empty in", base.Where(users.ID.In()), table + ` WHERE FALSE`, nil},
		{"in", base.Where(users.ID.In(1, 2)), table + ` WHERE "q"."id" IN ($1, $2)`, []any{int64(1), int64(2)}},
		{
			"subquery", database.From(base.Where(users.ID.Gt(3)).Limit(5)).Where(users.Email.Eq("a")),
			projection + `(` + table + ` WHERE "q"."id" > $1 LIMIT $2) AS "q" WHERE "q"."email" = $3`,
			[]any{int64(3), 5, "a"},
		},
		{
			"static source", database.From(ratel.NewQuery([]string{"id", "email", "manager_id"}, scanReadUser,
				`SELECT id, email, manager_id FROM app.users WHERE id > $1`, int64(4))).Where(users.ID.Lt(8)),
			projection + `(SELECT id, email, manager_id FROM app.users WHERE id > $1) AS "q" WHERE "q"."id" < $2`,
			[]any{int64(4), int64(8)},
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

func TestSelectInvalid(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	for _, tc := range []struct {
		name  string
		query ratel.Select[readUser]
	}{
		{"zero", ratel.Select[readUser]{}},
		{"negative limit", ratel.New(&pgx.Conn{}).From(users).Limit(-1)},
		{"negative offset", ratel.New(&pgx.Conn{}).From(users).Offset(-1)},
		{"zero column", ratel.New(&pgx.Conn{}).From(users).Where(ratel.Col[readUser, int64]{}.Eq(1))},
		{"zero order", ratel.New(&pgx.Conn{}).From(users).OrderBy(ratel.Order[readUser]{})},
		{"invalid nested source", ratel.New(&pgx.Conn{}).From(ratel.Select[readUser]{})},
		{"empty static source", ratel.New(&pgx.Conn{}).From(ratel.NewQuery([]string{"id"}, scanReadUser, ""))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.query.SQL()
			if !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestSelectImmutable(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	base := ratel.New(&pgx.Conn{}).From(users).Where(users.ID.Gt(0)).OrderBy(users.ID.Asc())

	for index := range 32 {
		t.Run(string(rune('A'+index)), func(t *testing.T) {
			t.Parallel()

			branch := base.Where(users.ID.Lt(int64(index))).OrderBy(users.Email.Desc())

			statement, err := branch.SQL()
			if err != nil || !reflect.DeepEqual(statement.Args, []any{int64(0), int64(index)}) {
				t.Fatalf("%v: %v", statement.Args, err)
			}
		})
	}
}

func TestReadErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err     error
		message string
	}{
		{ratel.ErrNotFound, "ratel: row not found"},
		{ratel.ErrMultipleRows, "ratel: more than one row"},
		{ratel.ErrInvalidQuery, "ratel: invalid query"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			t.Parallel()

			if tc.err.Error() != tc.message {
				t.Fatalf("got %v", tc.err)
			}
		})
	}
}

func BenchmarkSelect(b *testing.B) {
	users := userDescriptor()
	database := ratel.New(&pgx.Conn{})

	b.ReportAllocs()

	for b.Loop() {
		_, err := database.From(users).Where(users.ID.Gt(10)).OrderBy(users.ID.Desc()).Limit(20).SQL()
		if err != nil {
			b.Fatal(err)
		}
	}
}
