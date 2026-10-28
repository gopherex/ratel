package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func TestChunksInvalid(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		t.Fatal("invalid chunk query reached executor")

		return nil, errors.New("unexpected query")
	}}
	base := ratel.New(executor).From(users)

	for _, tc := range []struct {
		name  string
		query ratel.Select[readUser]
		size  int
	}{
		{"zero size", base, 0},
		{"negative size", base, -1},
		{"zero query", ratel.Select[readUser]{}, 1},
		{"wrong order", base.OrderBy(users.Email.Asc()), 1},
		{"descending", base.OrderBy(users.ID.Desc()), 1},
		{"extra order", base.OrderBy(users.ID.Asc(), users.Email.Asc()), 1},
		{"negative limit", base.Limit(-1), 1},
		{"negative offset", base.Offset(-1), 1},
		{"zero limit invalid column", base.Where(ratel.Col[readUser, int64]{}.Eq(1)).Limit(0), 1},
		{"zero limit invalid nested source", ratel.New(executor).From(base.Limit(-1)).Limit(0), 1},
		{"no key", ratel.New(executor).From(ratel.NewTable("app", "view", []string{"id"}, scanReadUser)), 1},
		{"missing key column", ratel.New(executor).From(ratel.NewTable("app", "users", []string{"id"}, scanReadUser,
			ratel.PrimaryKey([]string{"missing"}, func(user readUser) []any { return []any{user.ID} }))), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			count := 0

			for page, err := range tc.query.Chunks(t.Context(), tc.size) {
				count++

				if page != nil || !errors.Is(err, ratel.ErrInvalidQuery) {
					t.Fatalf("%v: %v", page, err)
				}
			}

			if count != 1 {
				t.Fatalf("yielded %d errors", count)
			}
		})
	}
}

func TestChunksLimitOffset(t *testing.T) {
	t.Parallel()

	calls := 0
	executor := readExecutor{query: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		calls++
		if calls == 1 {
			if !strings.HasSuffix(sql, `ORDER BY "q"."id" ASC LIMIT $1 OFFSET $2`) ||
				!reflect.DeepEqual(args, []any{2, 5}) {
				t.Fatalf("%s %v", sql, args)
			}

			return &readRows{ids: []int64{6, 7}}, nil
		}

		if calls != 2 || !strings.HasSuffix(sql, `WHERE ("q"."id") > ($1) ORDER BY "q"."id" ASC LIMIT $2`) ||
			!reflect.DeepEqual(args, []any{int64(7), 1}) {
			t.Fatalf("%s %v", sql, args)
		}

		return &readRows{ids: []int64{8}}, nil
	}}
	query := ratel.New(executor).From(userDescriptor()).Limit(3).Offset(5)

	for _, err := range query.Chunks(t.Context(), 2) {
		if err != nil {
			t.Fatal(err)
		}
	}

	if calls != 2 {
		t.Fatalf("queries = %d", calls)
	}
}

func TestChunksTermination(t *testing.T) {
	t.Parallel()

	failure := errors.New("offline")

	for _, tc := range []struct {
		name    string
		limit   int
		ids     []int64
		failure error
		stop    bool
		calls   int
	}{
		{"zero limit", 0, nil, nil, false, 0},
		{"empty", 5, nil, nil, false, 1},
		{"query failure", 5, nil, failure, false, 1},
		{"break", 5, []int64{1, 2}, nil, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
				calls++

				return &readRows{ids: tc.ids}, tc.failure
			}}
			query := ratel.New(executor).From(userDescriptor()).Limit(tc.limit)

			for _, err := range query.Chunks(t.Context(), 2) {
				if !errors.Is(err, tc.failure) {
					t.Fatal(err)
				}

				if tc.stop {
					break
				}
			}

			if calls != tc.calls {
				t.Fatalf("queries = %d", calls)
			}
		})
	}
}

func BenchmarkChunks(b *testing.B) {
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &readRows{ids: []int64{1}}, nil
	}}
	query := ratel.New(executor).From(userDescriptor()).Limit(1)

	b.ReportAllocs()

	for b.Loop() {
		for _, err := range query.Chunks(b.Context(), 2) {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
