package ratel_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

const rawPrefix = usersSQL + ` WHERE (`

// rawCase puts $1 inside a construct the scanner must skip and $2 after it:
// the first stays text, the second is renumbered to $1 and bound to "b".
type rawCase struct {
	name string
	sql  string
	want string
	args []any
}

func rawQuery(sql string, args ...any) ratel.Select[readUser] {
	return ratel.New(&pgx.Conn{}).From(userDescriptor()).Where(ratel.Raw[readUser](sql, args...))
}

func TestRawScanning(t *testing.T) {
	t.Parallel()

	second := []any{"b"}

	for _, tc := range []rawCase{
		{"plain markers", `$1 = $2`, `$1 = $2`, []any{"a", "b"}},
		{"markers renumbered by use", `$2 = $1`, `$1 = $2`, []any{"b", "a"}},
		{"single quoted", `'$1' = $2`, `'$1' = $1`, second},
		{"single quoted doubled quote", `'it''s $1' = $2`, `'it''s $1' = $1`, second},
		{"single quoted only doubled quotes", `'''' = $2`, `'''' = $1`, second},
		{"single quoted backslash is literal", `'\' = $2`, `'\' = $1`, second},
		{"E string escaped quote", `E'\'$1' = $2`, `E'\'$1' = $1`, second},
		{"lowercase e string escaped quote", `e'\'$1' = $2`, `e'\'$1' = $1`, second},
		{"E string escaped backslash", `E'\\' = $2`, `E'\\' = $1`, second},
		{"double quoted identifier", `"$1" = $2`, `"$1" = $1`, second},
		{"double quoted doubled quote", `"a""$1" = $2`, `"a""$1" = $1`, second},
		{"double quoted holds comment opener", `"a--$1" = $2`, `"a--$1" = $1`, second},
		{"dollar quoted", `$$ $1 $$ = $2`, `$$ $1 $$ = $1`, second},
		{"dollar quoted tag", `$t$ $1 $t$ = $2`, `$t$ $1 $t$ = $1`, second},
		{"dollar quoted holds quote", `$$ ' $1 $$ = $2`, `$$ ' $1 $$ = $1`, second},
		{"dollar quoted other tag inside", `$a$ $b$ $1 $b$ $a$ = $2`, `$a$ $b$ $1 $b$ $a$ = $1`, second},
		{"dollar quoted tag is case sensitive", `$tag$ $1 $TAG$ = $2`, `$tag$ $1 $TAG$ = $2`, nil},
		{"dollar with invalid tag is no quote", `$a-b$ = $2`, `$a-b$ = $1`, second},
		{"dollar without closing dollar", `$a = $2`, `$a = $1`, second},
		{"dollar without any later dollar", `$a`, `$a`, nil},
		{"dollar before marker", `$ = $2`, `$ = $1`, second},
		{"line comment", "x -- $1\n = $2", "x -- $1\n = $1", second},
		{"line comment unterminated", `x -- $1 = $2`, `x -- $1 = $2`, nil},
		{"block comment", `/* $1 */ = $2`, `/* $1 */ = $1`, second},
		{"block comment is not nested", `/* /* $1 */ = $2`, `/* /* $1 */ = $1`, second},
		{"block comment unterminated", `/* $1 = $2`, `/* $1 = $2`, nil},
		{"single quoted unterminated", `'$1 = $2`, `'$1 = $2`, nil},
		{"double quoted unterminated", `"$1 = $2`, `"$1 = $2`, nil},
		{"dollar quoted unterminated", `$$ $1 = $2`, `$$ $1 = $2`, nil},
		{"empty", ``, ``, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := rawQuery(tc.sql, "a", "b").SQL()
			want := rawPrefix + tc.want + `)`

			if err != nil || statement.SQL != want || !reflect.DeepEqual(statement.Args, tc.args) {
				t.Fatalf("got %+v, %v; want %s, %#v", statement, err, want, tc.args)
			}
		})
	}
}

func TestRawInvalidMarker(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"beyond arguments", `$3 = 1`},
		{"zero", `$0 = 1`},
		{"overflow", `$99999999999999999999 = 1`},
		{"after literal", `'x' = $3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := rawQuery(tc.sql, "a", "b").SQL(); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("SQL() error = %v", err)
			}
		})
	}
}
