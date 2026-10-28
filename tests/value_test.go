//go:build integration

package tests_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gopherex/ratel"
)

type jsonValue struct {
	Name string `json:"name"`
}

func TestValueRoundTrip(t *testing.T) {
	t.Parallel()

	pool := valuePool(t)

	for _, tc := range []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool)
	}{
		{"int8", roundTripCase("int8", int64(42))},
		{"text", roundTripCase("text", "hello")},
		{"timestamptz", roundTripCase("timestamptz", time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))},
		{"jsonb", roundTripCase("jsonb", jsonValue{Name: "hello"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, pool)
		})
	}
}

func TestValueConnectionFailure(t *testing.T) {
	t.Parallel()

	pool := valuePool(t)

	ctx, cancel := context.WithTimeout(t.Context(), containerWait)
	defer cancel()

	var (
		terminated  bool
		serverError *pgconn.PgError
	)

	err := pool.QueryRow(ctx, "SELECT pg_catalog.pg_terminate_backend(pg_catalog.pg_backend_pid())").Scan(&terminated)
	if !errors.As(err, &serverError) || serverError.Code != "57P01" {
		t.Fatalf("expected admin_shutdown, got %v", err)
	}

	var value ratel.Null[int64]
	if err := pool.QueryRow(ctx, "SELECT $1::pg_catalog.int8", ratel.NullFrom(int64(42))).Scan(&value); err != nil {
		t.Fatal(err)
	}

	if !value.Valid || value.V != 42 {
		t.Fatalf("optional codecs lost after reconnect: %+v", value)
	}
}

func roundTripCase[T any](name string, value T) func(*testing.T, *pgxpool.Pool) {
	return func(t *testing.T, pool *pgxpool.Pool) {
		t.Helper()
		roundTripValue(t, pool, name, value)
	}
}

func roundTripValue[T any](t *testing.T, pool *pgxpool.Pool, name string, value T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), containerWait)
	defer cancel()

	query := "SELECT $1::pg_catalog." + name + ", $1::pg_catalog." + name

	for _, mode := range []pgx.QueryExecMode{
		pgx.QueryExecModeCacheStatement, pgx.QueryExecModeExec, pgx.QueryExecModeSimpleProtocol,
	} {
		for _, format := range []int16{pgtype.TextFormatCode, pgtype.BinaryFormatCode} {
			for _, input := range []any{ratel.NullFrom(value), ratel.OptFrom(value), ratel.OptFrom(ratel.NullFrom(value))} {
				var (
					nullable ratel.Null[T]
					plain    T
				)

				err := pool.QueryRow(ctx, query, mode, pgx.QueryResultFormats{format, format}, input).Scan(&nullable, &plain)
				if err != nil || !nullable.Valid || !reflect.DeepEqual(nullable.V, plain) {
					t.Fatalf("%v format %d %T: %+v: %v", mode, format, input, nullable, err)
				}
			}

			for _, input := range []any{ratel.Null[T]{V: value}, ratel.OptFrom(ratel.Null[T]{})} {
				nullable := ratel.NullFrom(value)
				optional := ratel.OptFrom(ratel.NullFrom(value))

				err := pool.QueryRow(ctx, query, mode, pgx.QueryResultFormats{format, format}, input).Scan(&nullable, &optional)
				if err != nil || nullable.Valid || !optional.Set || optional.V.Valid {
					t.Fatalf("%v format %d NULL: %+v %+v: %v", mode, format, nullable, optional, err)
				}
			}
		}

		var (
			target ratel.Null[T]
			unset  ratel.UnsetOptError
		)

		err := pool.QueryRow(ctx, query, mode, ratel.Opt[T]{}).Scan(&target, &target)
		if !errors.As(err, &unset) {
			t.Fatalf("%v unset optional: %v", mode, err)
		}
	}
}

func valuePool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()

	image := os.Getenv("RATEL_PG_IMAGE")
	if image == "" {
		image = defaultImage
	}

	ctx, cancel := context.WithTimeout(tb.Context(), containerWait)
	defer cancel()

	container, err := postgres.Run(ctx, image, testcontainers.WithWaitStrategy(
		wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(containerWait),
	))
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), containerWait)
		defer cleanupCancel()

		if err := container.Terminate(cleanupCtx); err != nil {
			tb.Errorf("terminate PostgreSQL: %v", err)
		}
	})

	config, err := pgxpool.ParseConfig(container.MustConnectionString(ctx))
	if err != nil {
		tb.Fatal(err)
	}

	config.AfterConnect = registerValues

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(pool.Close)

	return pool
}

func registerValues(_ context.Context, conn *pgx.Conn) error {
	typeMap := conn.TypeMap()
	if err := ratel.RegisterValue[int64](typeMap, "int8"); err != nil {
		return err
	}

	if err := ratel.RegisterValue[string](typeMap, "text"); err != nil {
		return err
	}

	if err := ratel.RegisterValue[time.Time](typeMap, "timestamptz"); err != nil {
		return err
	}

	return ratel.RegisterValue[jsonValue](typeMap, "jsonb")
}
