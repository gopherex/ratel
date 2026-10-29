package ratel_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/pkg/ratel"
)

const usersSQL = `SELECT "q"."id", "q"."email", "q"."manager_id" FROM "app"."users" AS "q"`

func TestSelectClauses(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	base := ratel.From(users)

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

			statement, err := build(tc.query)
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

	query := ratel.From(users).Join(orders, users.ID.EqCol(userID)).Where(users.ID.Gt(1)).
		Join(ratel.From(orders).Where(orderID.Gt(9)), users.ID.EqCol(userID))

	statement, err := build(query)
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
	base := ratel.From(users)

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

			if _, err := build(tc.query); !errors.Is(err, ratel.ErrInvalidQuery) {
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

	count, err := ratel.New(scalarExecutor{sql: &sql, value: int64(7)}).Run(ratel.From(users).Limit(10)).Count(t.Context())
	if err != nil || count != 7 || sql != `SELECT count(*) FROM (`+usersSQL+` LIMIT $1) AS "c"` {
		t.Fatalf("Count() = %d, %v; SQL %s", count, err, sql)
	}

	database := ratel.New(scalarExecutor{sql: &sql, value: true})

	exists, err := database.Run(ratel.From(users).Where(users.ID.Eq(1))).Exists(t.Context())
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

	ids, err := ratel.New(executor).Run(ratel.From(users).OrderBy(users.ID.Asc()).Pluck(users.ID)).List(t.Context())
	if err != nil || !reflect.DeepEqual(ids, []int64{3, 4}) ||
		sql != `SELECT "p"."id" FROM (`+usersSQL+` ORDER BY "q"."id" ASC) AS "p"` || !rows.closed {
		t.Fatalf("Pluck() = %v, %v; SQL %s", ids, err, sql)
	}
}

func TestPluckBuild(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	got, err := build(ratel.From(users).Where(users.ID.Gt(1)).Pluck(users.ID))
	if err != nil || got.SQL != `SELECT "p"."id" FROM (`+usersSQL+` WHERE "q"."id" > $1) AS "p"` ||
		!reflect.DeepEqual(got.Args, []any{int64(1)}) {
		t.Fatalf("Build() = %+v, %v", got, err)
	}

	if _, err = build(ratel.Plucked[int64]{}); !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("zero Build() = %v", err)
	}
}

// selXRows serves int64 and nullable int64 values to Scan.
type selXRows struct {
	pgx.Rows
	values      []any
	index       int
	closed      bool
	failure     error
	scanFailure error
}

func (r *selXRows) Next() bool {
	if r.index == len(r.values) {
		return false
	}

	r.index++

	return true
}

func (r *selXRows) Scan(dest ...any) error {
	if r.scanFailure != nil {
		return r.scanFailure
	}

	switch target := dest[0].(type) {
	case *int64:
		if value, ok := r.values[r.index-1].(int64); ok {
			*target = value
		}
	case **int64:
		if value, ok := r.values[r.index-1].(*int64); ok {
			*target = value
		}
	default:
		return errors.New("unexpected scan target")
	}

	return nil
}

func (r *selXRows) Close()     { r.closed = true }
func (r *selXRows) Err() error { return r.failure }

type selXRow struct{ err error }

func (r selXRow) Scan(...any) error { return r.err }

type selXScalarExecutor struct {
	ratel.Executor
	row pgx.Row
}

func (e selXScalarExecutor) QueryRow(context.Context, string, ...any) pgx.Row { return e.row }

// selXDrain collects values and errors; stopAfter > 0 breaks after that many values.
func selXDrain[V any](seq iter.Seq2[V, error], stopAfter int) ([]V, []error) {
	var (
		values []V
		errs   []error
	)

	for value, err := range seq {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		values = append(values, value)
		if len(values) == stopAfter {
			break
		}
	}

	return values, errs
}

// selXExpectErrors requires no error when want is nil, else exactly one matching it.
func selXExpectErrors(t *testing.T, errs []error, want error) {
	t.Helper()

	if want == nil && len(errs) != 0 {
		t.Fatalf("unexpected errors %v", errs)
	}

	if want != nil && (len(errs) != 1 || !errors.Is(errs[0], want)) {
		t.Fatalf("errors = %v, want exactly one %v", errs, want)
	}
}

func TestScalarFailures(t *testing.T) {
	t.Parallel()

	offline := errors.New("offline")
	users := userDescriptor()

	for _, tc := range []struct {
		name  string
		build func() (int64, error)
		want  error
	}{
		{
			"count no database",
			func() (int64, error) { return (*ratel.DB)(nil).Run(ratel.Select[readUser]{}).Count(t.Context()) },
			ratel.ErrInvalidQuery,
		},
		{
			"count invalid query",
			func() (int64, error) {
				return ratel.New(selXScalarExecutor{row: selXRow{}}).Run(ratel.From(users).SkipLocked()).Count(t.Context())
			},
			ratel.ErrInvalidQuery,
		},
		{
			"count scan error",
			func() (int64, error) {
				return ratel.New(selXScalarExecutor{row: selXRow{err: offline}}).Run(ratel.From(users)).Count(t.Context())
			},
			offline,
		},
		{
			"exists no database",
			func() (int64, error) {
				_, err := (*ratel.DB)(nil).Run(ratel.Select[readUser]{}).Exists(t.Context())

				return 0, err
			},
			ratel.ErrInvalidQuery,
		},
		{
			"exists scan error",
			func() (int64, error) {
				_, err := ratel.New(selXScalarExecutor{row: selXRow{err: offline}}).Run(ratel.From(users)).Exists(t.Context())

				return 0, err
			},
			offline,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			count, err := tc.build()
			if !errors.Is(err, tc.want) || count != 0 {
				t.Fatalf("count = %d, error = %v, want %v", count, err, tc.want)
			}
		})
	}
}

func TestJoinInvalidSource(t *testing.T) {
	t.Parallel()

	users, orders := relationDescriptors()
	userID := ratel.NewCol[relationOrder, int64]("user_id")

	for name, source := range map[string]ratel.Source[relationOrder]{
		"invalid table":    orders.InSchema(""),
		"invalid subquery": ratel.From(orders).SkipLocked(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			query := ratel.From(users).Join(source, users.ID.EqCol(userID))
			if _, err := build(query); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("SQL() error = %v", err)
			}
		})
	}
}

func TestPluckNull(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	three, five := int64(3), int64(5)
	rows := &selXRows{values: []any{&three, (*int64)(nil), &five}}

	var (
		sql  string
		args []any
	)

	executor := readExecutor{query: func(_ context.Context, got string, given ...any) (pgx.Rows, error) {
		sql, args = got, given

		return rows, nil
	}}

	managers, err := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeExec)).Run(ratel.From(users).
		Where(users.ID.Gt(1)).OrderBy(users.ID.Asc()).PluckNull(users.ManagerID)).List(t.Context())

	want := []ratel.Null[int64]{{V: 3, Valid: true}, {}, {V: 5, Valid: true}}
	wantSQL := `SELECT "p"."manager_id" FROM (` + usersSQL + ` WHERE "q"."id" > $1 ORDER BY "q"."id" ASC) AS "p"`

	if err != nil || !reflect.DeepEqual(managers, want) || sql != wantSQL || !rows.closed {
		t.Fatalf("PluckNull() = %v, %v; SQL %s", managers, err, sql)
	}

	if !reflect.DeepEqual(args, []any{pgx.QueryExecModeExec, int64(1)}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestPluckedAll(t *testing.T) {
	t.Parallel()

	failure := errors.New("connection lost")
	users := userDescriptor()

	for _, tc := range []struct {
		name        string
		zero        bool
		invalid     bool
		badColumn   bool
		queryError  error
		values      []any
		failure     error
		scanFailure error
		stopAfter   int
		wantValues  []int64
		wantError   error
		wantQueried bool
		wantClosed  bool
	}{
		{name: "no database", zero: true, wantError: ratel.ErrInvalidQuery},
		{name: "invalid query", invalid: true, wantError: ratel.ErrInvalidQuery},
		{name: "invalid column", badColumn: true, wantError: ratel.ErrInvalidQuery},
		{name: "query error", queryError: failure, wantError: failure, wantQueried: true},
		{
			name: "scan error", values: []any{int64(1)}, scanFailure: failure, wantError: failure,
			wantQueried: true, wantClosed: true,
		},
		{
			name: "rows error", values: []any{int64(1), int64(2)}, failure: failure, wantValues: []int64{1, 2},
			wantError: failure, wantQueried: true, wantClosed: true,
		},
		{
			name: "early stop", values: []any{int64(1), int64(2), int64(3)}, stopAfter: 2, wantValues: []int64{1, 2},
			wantQueried: true, wantClosed: true,
		},
		{
			name: "all values", values: []any{int64(1), int64(2)}, wantValues: []int64{1, 2},
			wantQueried: true, wantClosed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows := &selXRows{values: tc.values, failure: tc.failure, scanFailure: tc.scanFailure}

			var queried bool

			executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
				queried = true

				if tc.queryError != nil {
					return nil, tc.queryError
				}

				return rows, nil
			}}

			query := ratel.From(users)
			if tc.invalid {
				query = query.SkipLocked()
			}

			plucked := query.Pluck(users.ID)
			if tc.badColumn {
				plucked = query.Pluck(ratel.NewCol[readUser, int64](""))
			}

			if tc.zero {
				plucked = ratel.Plucked[int64]{}
			}

			got, errs := selXDrain(ratel.New(executor).Run(plucked).All(t.Context()), tc.stopAfter)
			if len(got) != len(tc.wantValues) || (len(got) > 0 && !reflect.DeepEqual(got, tc.wantValues)) {
				t.Fatalf("values = %v, want %v", got, tc.wantValues)
			}

			selXExpectErrors(t, errs, tc.wantError)

			if queried != tc.wantQueried || rows.closed != tc.wantClosed {
				t.Fatalf("queried = %v closed = %v", queried, rows.closed)
			}
		})
	}
}

func TestPluckedListErrors(t *testing.T) {
	t.Parallel()

	failure := errors.New("connection lost")
	users := userDescriptor()
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &selXRows{values: []any{int64(1)}, failure: failure}, nil
	}}

	values, err := ratel.New(executor).Run(ratel.From(users).Pluck(users.ID)).List(t.Context())
	if !errors.Is(err, failure) || values != nil {
		t.Fatalf("List() = %v, %v", values, err)
	}

	empty, err := ratel.New(readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &selXRows{}, nil
	}}).Run(ratel.From(users).Pluck(users.ID)).List(t.Context())
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty List() = %#v, %v", empty, err)
	}
}

func BenchmarkPluckNull(b *testing.B) {
	users := userDescriptor()
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &selXRows{values: []any{ratel.NullFrom(int64(3)), ratel.Null[int64]{}}}, nil
	}}
	plucked := ratel.From(users).OrderBy(users.ID.Asc()).PluckNull(users.ManagerID)

	database := ratel.New(executor)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := database.Run(plucked).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKeyset(b *testing.B) {
	users := userDescriptor()
	query := ratel.From(users).OrderBy(users.Email.Desc(), users.ID.Asc()).After("m", int64(5))

	for b.Loop() {
		if _, err := build(query); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRaw(b *testing.B) {
	users := userDescriptor()

	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"quoted", `"q"."email" ~ $1 AND '$1' <> $2`, []any{"a", "b"}},
		{"comments", "/* a /* nested $1 */ $1 */ \"q\".\"id\" = $1 -- tail $2\n AND TRUE", []any{1}},
		{"dollar quotes", `$tag$ $1 $tag$ <> "q"."email" AND $$ $2 $$ <> $1`, []any{"a"}},
		{"escape strings", `E'\'$1' <> "q"."email" AND e'a\\' <> $1`, []any{"a"}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			query := ratel.From(users).Where(ratel.Raw[readUser](tc.sql, tc.args...))

			b.ReportAllocs()

			for b.Loop() {
				if _, err := build(query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSelectJoinLock(b *testing.B) {
	users, orders := relationDescriptors()
	userID := ratel.NewCol[relationOrder, int64]("user_id")
	base := ratel.From(users)

	cases := []struct {
		name  string
		query ratel.Select[relationUser]
	}{
		{"join", base.Join(orders, users.ID.EqCol(userID))},
		{"join subquery", base.Join(ratel.From(orders).Where(userID.Gt(9)), users.ID.EqCol(userID))},
		{"for update", base.ForUpdate()},
		{"for share", base.ForShare()},
		{"skip locked", base.ForUpdate().SkipLocked()},
		{"no wait", base.ForShare().NoWait()},
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

func BenchmarkSelectScalars(b *testing.B) {
	users := userDescriptor()
	sql := ""
	counted := ratel.From(users).Where(users.ID.Gt(1))
	present := ratel.From(users).Where(users.ID.Gt(1))
	plucked := ratel.From(users).OrderBy(users.ID.Asc()).Pluck(users.ID)
	countDB := ratel.New(scalarExecutor{sql: &sql, value: int64(7)})
	existsDB := ratel.New(scalarExecutor{sql: &sql, value: true})
	pluckDB := ratel.New(readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &selXRows{values: []any{int64(3), int64(4)}}, nil
	}})

	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"count", func(ctx context.Context) error {
			_, err := countDB.Run(counted).Count(ctx)

			return err
		}},
		{"exists", func(ctx context.Context) error {
			_, err := existsDB.Run(present).Exists(ctx)

			return err
		}},
		{"pluck", func(ctx context.Context) error {
			_, err := pluckDB.Run(plucked).List(ctx)

			return err
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if err := tc.run(b.Context()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
