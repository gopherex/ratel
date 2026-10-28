package ratel_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

type readExecutor struct {
	ratel.Executor
	query func(context.Context, string, ...any) (pgx.Rows, error)
}

func (e readExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return e.query(ctx, sql, args...)
}

type readRows struct {
	pgx.Rows
	ids         []int64
	index       int
	closed      bool
	failure     error
	scanFailure error
}

func (r *readRows) Next() bool {
	if r.index == len(r.ids) {
		return false
	}

	r.index++

	return true
}

func (r *readRows) Scan(dest ...any) error {
	if r.scanFailure != nil {
		return r.scanFailure
	}

	switch id := dest[0].(type) {
	case *int64:
		*id = r.ids[r.index-1]
	default:
		return fmt.Errorf("unexpected scan type %T", dest[0])
	}

	return nil
}

func (r *readRows) Close()     { r.closed = true }
func (r *readRows) Err() error { return r.failure }

func TestReadTerminals(t *testing.T) {
	t.Parallel()

	failure := errors.New("connection lost")
	for _, tc := range []struct {
		name        string
		ids         []int64
		terminal    string
		failure     error
		scanFailure error
		want        int
		wantError   error
	}{
		{name: "list", ids: []int64{1, 2}, terminal: "list", want: 2},
		{name: "empty list", terminal: "list"},
		{name: "one", ids: []int64{1}, terminal: "one", want: 1},
		{name: "not found", terminal: "one", wantError: ratel.ErrNotFound},
		{name: "multiple", ids: []int64{1, 2}, terminal: "one", wantError: ratel.ErrMultipleRows},
		{name: "first", ids: []int64{1}, terminal: "first", want: 1},
		{name: "first empty", terminal: "first", wantError: ratel.ErrNotFound},
		{name: "iteration error", terminal: "list", failure: failure, wantError: failure},
		{name: "scan error", ids: []int64{1}, terminal: "list", scanFailure: failure, wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows := &readRows{ids: tc.ids, failure: tc.failure, scanFailure: tc.scanFailure}
			executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil }}
			query := ratel.New(executor).From(userDescriptor())

			var count int

			var err error

			switch tc.terminal {
			case "one":
				_, err = query.One(t.Context())
				if err == nil {
					count = 1
				}
			case "first":
				_, err = query.First(t.Context())
				if err == nil {
					count = 1
				}
			default:
				var users []readUser
				users, err = query.List(t.Context())
				count = len(users)
			}

			if !errors.Is(err, tc.wantError) || count != tc.want || !rows.closed {
				t.Fatalf("count=%d error=%v closed=%v", count, err, rows.closed)
			}
		})
	}
}

func TestAllStopsAndReleases(t *testing.T) {
	t.Parallel()

	rows := &readRows{ids: []int64{1, 2}}

	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil }}
	for user, err := range ratel.New(executor).From(userDescriptor()).All(t.Context()) {
		if err != nil || user.ID != 1 {
			t.Fatalf("%+v: %v", user, err)
		}

		break
	}

	if !rows.closed || rows.index != 1 {
		t.Fatal("cursor was not released on early break")
	}
}

func TestReadQueryFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("offline")
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, failure }}

	_, err := ratel.New(executor).From(userDescriptor()).List(t.Context())
	if !errors.Is(err, failure) {
		t.Fatalf("lost cause: %v", err)
	}
}

func TestReadExecMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []pgx.QueryExecMode{pgx.QueryExecModeExec, pgx.QueryExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			rows := &readRows{}
			executor := readExecutor{query: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
				if !reflect.DeepEqual(args, []any{mode, int64(42)}) {
					t.Fatalf("args: %#v", args)
				}

				return rows, nil
			}}
			users := userDescriptor()

			_, err := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeCacheDescribe)).From(users).
				Where(users.ID.Eq(42)).List(t.Context(), ratel.ExecMode(mode))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChunks(t *testing.T) {
	t.Parallel()

	var previous *readRows

	var calls int

	executor := readExecutor{query: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		if previous != nil && !previous.closed {
			t.Fatal("page cursor still open")
		}
		calls++
		switch calls {
		case 1:
			if !reflect.DeepEqual(args, []any{2}) {
				t.Fatalf("first page: %s %v", sql, args)
			}
			previous = &readRows{ids: []int64{1, 2}}
		case 2:
			if !reflect.DeepEqual(args, []any{int64(2), 2}) {
				t.Fatalf("second page: %s %v", sql, args)
			}
			previous = &readRows{ids: []int64{3}}
		default:
			t.Fatal("unexpected page")
		}

		return previous, nil
	}}

	var ids []int64

	for page, err := range ratel.New(executor).From(userDescriptor()).Chunks(t.Context(), 2) {
		if err != nil {
			t.Fatal(err)
		}

		if !previous.closed {
			t.Fatal("cursor held while yielding page")
		}

		for _, user := range page {
			ids = append(ids, user.ID)
		}
	}

	if !reflect.DeepEqual(ids, []int64{1, 2, 3}) || calls != 2 {
		t.Fatalf("%v (%d queries)", ids, calls)
	}
}

func BenchmarkReadTerminals(b *testing.B) {
	for _, name := range []string{"list", "all", "first", "failure"} {
		b.Run(name, func(b *testing.B) {
			benchmarkReadTerminal(b, name)
		})
	}
}

func benchmarkReadTerminal(b *testing.B, name string) {
	b.Helper()

	failure := errors.New("offline")
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		if name == "failure" {
			return nil, failure
		}

		return &readRows{ids: []int64{1}}, nil
	}}
	query := ratel.New(executor, ratel.WithExecMode(pgx.QueryExecModeExec)).From(userDescriptor())

	b.ReportAllocs()

	for b.Loop() {
		var err error

		switch name {
		case "list", "failure":
			_, err = query.List(b.Context())
		case "first":
			_, err = query.First(b.Context())
		default:
			for _, rowError := range query.All(b.Context()) {
				err = rowError
			}
		}

		if err != nil && !errors.Is(err, failure) {
			b.Fatal(err)
		}
	}
}

func BenchmarkRead(b *testing.B) {
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &readRows{ids: []int64{1}}, nil
	}}
	query := ratel.New(executor).From(userDescriptor())

	b.ReportAllocs()

	for b.Loop() {
		if _, err := query.One(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
