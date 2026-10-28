package ratel_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestColumnOperators(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	for _, tc := range []struct {
		name      string
		condition ratel.Cond[readUser]
		sql       string
		args      []any
	}{
		{"eq", users.ID.Eq(1), `"q"."id" = $1`, []any{int64(1)}},
		{"ne", users.ID.Ne(1), `"q"."id" <> $1`, []any{int64(1)}},
		{"lt", users.ID.Lt(1), `"q"."id" < $1`, []any{int64(1)}},
		{"lte", users.ID.Lte(1), `"q"."id" <= $1`, []any{int64(1)}},
		{"gt", users.ID.Gt(1), `"q"."id" > $1`, []any{int64(1)}},
		{"gte", users.ID.Gte(1), `"q"."id" >= $1`, []any{int64(1)}},
		{"like", users.Email.Like("x%"), `"q"."email" LIKE $1`, []any{"x%"}},
		{"not like", users.Email.NotLike("x%"), `"q"."email" NOT LIKE $1`, []any{"x%"}},
		{"ilike", users.Email.ILike("x%"), `"q"."email" ILIKE $1`, []any{"x%"}},
		{"range", users.ID.Between(1, 2), `"q"."id" BETWEEN $1 AND $2`, []any{int64(1), int64(2)}},
		{"not in", users.ID.NotIn(1, 2), `"q"."id" NOT IN ($1, $2)`, []any{int64(1), int64(2)}},
		{"not in empty", users.ID.NotIn(), `TRUE`, nil},
		{"null", users.ManagerID.IsNull(), `"q"."manager_id" IS NULL`, nil},
		{"not null", users.ManagerID.IsNotNull(), `"q"."manager_id" IS NOT NULL`, nil},
		{"nullable eq", users.ManagerID.Eq(1), `"q"."manager_id" = $1`, []any{int64(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := ratel.New(&pgx.Conn{}).From(users).Where(tc.condition).SQL()
			if err != nil || !strings.HasSuffix(statement.SQL, " WHERE "+tc.sql) ||
				!reflect.DeepEqual(statement.Args, tc.args) {
				t.Fatalf("%+v: %v", statement, err)
			}
		})
	}
}

func TestColumnComparison(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	condition := users.ManagerID.EqCol(users.ID).And(users.ID.GtCol(users.ManagerID)).Not()

	statement, err := condition.SQL("employee", "manager")
	if err != nil || statement.SQL !=
		`NOT (("employee"."manager_id" = "manager"."id" AND "employee"."id" > "manager"."manager_id"))` {
		t.Fatalf("%+v: %v", statement, err)
	}
}

func TestColumnComparisons(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	for _, tc := range []struct {
		name      string
		condition ratel.Cond2[readUser, readUser]
		operator  string
	}{
		{"eq", users.ID.EqCol(users.ManagerID), " = "},
		{"ne", users.ID.NeCol(users.ManagerID), " <> "},
		{"lt", users.ID.LtCol(users.ManagerID), " < "},
		{"lte", users.ID.LteCol(users.ManagerID), " <= "},
		{"gt", users.ID.GtCol(users.ManagerID), " > "},
		{"gte", users.ID.GteCol(users.ManagerID), " >= "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := tc.condition.SQL("left", "right")
			if err != nil || statement.SQL != `"left"."id"`+tc.operator+`"right"."manager_id"` {
				t.Fatalf("%+v: %v", statement, err)
			}
		})
	}
}

func TestMembershipCopiesInputs(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	values := []int64{1, 2}
	condition := users.ID.In(values...)
	values[0] = 99

	statement, err := ratel.New(&pgx.Conn{}).From(users).Where(condition).SQL()
	if err != nil || !reflect.DeepEqual(statement.Args, []any{int64(1), int64(2)}) {
		t.Fatalf("%+v: %v", statement, err)
	}
}

func BenchmarkColumnComparisons(b *testing.B) {
	users := userDescriptor()

	b.ReportAllocs()

	for b.Loop() {
		_, err := users.ID.NeCol(users.ManagerID).Or(users.ID.LteCol(users.ID)).SQL("left", "right")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkColumnRange(b *testing.B) {
	users := userDescriptor()
	database := ratel.New(&pgx.Conn{})

	b.ReportAllocs()

	for b.Loop() {
		_, err := database.From(users).Where(users.ID.Between(1, 2)).SQL()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkColumnOperators(b *testing.B) {
	users := userDescriptor()
	database := ratel.New(&pgx.Conn{})

	b.ReportAllocs()

	for b.Loop() {
		_, err := database.From(users).Where(users.ID.In(1, 2).And(users.ManagerID.IsNotNull())).SQL()
		if err != nil {
			b.Fatal(err)
		}
	}
}
