//go:build integration

package rateltest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel"
	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/rateltest"
)

func TestRateltestPostgres(t *testing.T) {
	t.Parallel()

	recorder := &rateltest.Recorder{}
	server := rateltest.NewServer(t,
		rateltest.WithTimeout(time.Minute),
		rateltest.WithMigrations(
			[]string{
				`CREATE SCHEMA app; CREATE TABLE app.items (id bigint PRIMARY KEY, name text)`,
				`INSERT INTO app.items VALUES (1, 'before')`,
			},
			[]string{`UPDATE app.items SET name = 'after'`},
			[]string{`ALTER TABLE app.items ALTER COLUMN name SET NOT NULL`},
		),
		rateltest.WithPoolConfig(func(config *pgxpool.Config) {
			config.ConnConfig.Tracer = recorder
		}),
		rateltest.WithPoolConfig(func(config *pgxpool.Config) {
			config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
				return ratel.RegisterValue[string](conn.TypeMap(), "text")
			}
		}),
	)

	t.Cleanup(func() {
		counts := make(map[string]int)
		for _, statement := range recorder.Queries() {
			counts[statement.SQL]++
		}

		for _, sql := range []string{
			`SELECT name FROM app.items WHERE id = 1`,
			`INSERT INTO app.items VALUES (2, $1)`,
			`SELECT id FROM app.items WHERE id = 1`,
			`COPY "app"."items" ("id", "name") FROM STDIN`,
		} {
			if counts[sql] != 3 {
				t.Errorf("trace count for %q = %d, want 3", sql, counts[sql])
			}
		}
	})

	for _, name := range []string{"first", "second", "third"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var pool *pgxpool.Pool

			t.Cleanup(func() { assertDatabaseDropped(t, pool) })

			pool = server.Postgres(t)

			var value ratel.Null[string]

			err := pool.QueryRow(t.Context(), `SELECT name FROM app.items WHERE id = 1`).Scan(&value)
			if err != nil || value.V != "after" {
				t.Fatalf("template value = %v, %v", value, err)
			}

			if _, err := pool.Exec(t.Context(), `INSERT INTO app.items VALUES (2, $1)`, ratel.NullFrom(name)); err != nil {
				t.Fatal(err)
			}

			traceBatchAndCopy(t, pool)
		})
	}
}

func traceBatchAndCopy(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	batch := &pgx.Batch{}
	batch.Queue(`SELECT id FROM app.items WHERE id = 1`)
	result := pool.SendBatch(t.Context(), batch)

	var id int64
	if err := result.QueryRow().Scan(&id); err != nil {
		t.Fatal(err)
	}

	if err := result.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.CopyFrom(t.Context(), pgx.Identifier{"app", "items"}, []string{"id", "name"},
		pgx.CopyFromRows([][]any{{int64(3), "copy"}})); err != nil {
		t.Fatal(err)
	}
}

func assertDatabaseDropped(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	if pool == nil {
		return
	}

	config := pool.Config().ConnConfig.Copy()
	name := config.Database
	config.Database = "postgres"

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()

	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)

	var exists bool
	if err := connection.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_database WHERE datname = $1)`, name).
		Scan(&exists); err != nil || exists {
		t.Fatalf("database %q survived cleanup: exists=%v, error=%v", name, exists, err)
	}
}

func TestRateltestRollback(t *testing.T) {
	t.Parallel()

	pool := rateltest.Postgres(t)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE public.items (id bigint)`); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
		defer cancel()

		var count int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.items`).Scan(&count); err != nil || count != 0 {
			t.Errorf("after rollback = %d, %v", count, err)
		}
	})
	t.Run("transaction", func(t *testing.T) {
		t.Parallel()

		transaction := rateltest.RollbackTx(t, pool, time.Minute)
		if _, err := transaction.Exec(t.Context(), `INSERT INTO public.items VALUES (1)`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("already rolled back", func(t *testing.T) {
		t.Parallel()

		transaction := rateltest.RollbackTx(t, pool)
		if err := transaction.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestRateltestMigrationsDir migrates the template with a migrations
// directory, between raw SQL options, in option order.
func TestRateltestMigrationsDir(t *testing.T) {
	t.Parallel()

	file := `-- ratel:step id=a1 phase=expand tx=in class=short by=hand locks=app.items:AccessExclusive pg=16
ALTER TABLE app.items ADD COLUMN name text;
-- ratel:step id=a2 phase=backfill tx=in class=backfill pg=16 key=id
UPDATE app.items SET name = 'item ' || id WHERE name IS NULL;
-- ratel:step id=a3 phase=contract tx=in class=short locks=app.items:AccessExclusive pg=16
ALTER TABLE app.items ALTER COLUMN name SET NOT NULL;
`
	migrations := fstest.MapFS{
		"1_name.sql":    {Data: []byte(file)},
		migrate.SumFile: {Data: migrate.Sum([]migrate.Source{{Name: "1_name.sql", SQL: file}})},
	}

	server := rateltest.NewServer(t,
		rateltest.WithMigrations([]string{
			`CREATE SCHEMA app; CREATE TABLE app.items (id bigint PRIMARY KEY); INSERT INTO app.items VALUES (1), (2)`,
		}, nil, nil),
		rateltest.WithMigrationsDir(migrations),
		rateltest.WithMigrations([]string{`INSERT INTO app.items VALUES (3, 'three')`}, nil, nil),
	)

	pool := server.Postgres(t)

	var names string

	err := pool.QueryRow(t.Context(), `SELECT string_agg(name, ',' ORDER BY id) FROM app.items`).Scan(&names)
	if err != nil || names != "item 1,item 2,three" {
		t.Fatalf("names = %q, %v", names, err)
	}

	var steps int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ratel.migrations_journal WHERE outcome = 'done'`).
		Scan(&steps); err != nil || steps != 3 {
		t.Fatalf("journal = %d, %v", steps, err)
	}
}

func BenchmarkNewServer(b *testing.B) {
	for b.Loop() {
		func() {
			fixture := &fixtureTB{TB: b}
			defer fixture.close()

			_ = rateltest.NewServer(fixture)
		}()
	}
}

func BenchmarkPostgres(b *testing.B) {
	server := rateltest.NewServer(b)

	for b.Loop() {
		func() {
			fixture := &fixtureTB{TB: b}
			defer fixture.close()

			_ = server.Postgres(fixture)
		}()
	}
}

func TestRateltestFailureAndRecovery(t *testing.T) {
	t.Parallel()

	pool := rateltest.Postgres(t)
	executor := rateltest.FailWith("55P03").Wrap(pool, func(sql string) bool { return strings.Contains(sql, "blocked") })
	_, err := executor.Exec(t.Context(), `SELECT 'blocked'`)

	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "55P03" {
		t.Fatalf("injected error = %v", err)
	}

	transaction := rateltest.RollbackTx(t, pool)
	if _, err = transaction.Exec(t.Context(), `SELECT 1 / 0`); err == nil {
		t.Fatal("expected real PostgreSQL failure")
	}

	if err = transaction.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err = executor.Exec(t.Context(), `SELECT 1`); err != nil {
		t.Fatalf("pool did not recover: %v", err)
	}

	connection, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	pid := connection.Conn().PgConn().PID()
	if _, err = pool.Exec(t.Context(), `SELECT pg_terminate_backend($1)`, pid); err != nil {
		connection.Release()
		t.Fatal(err)
	}

	if _, err = connection.Exec(t.Context(), `SELECT 1`); err == nil {
		connection.Release()
		t.Fatal("terminated connection succeeded")
	}

	connection.Release()

	if err = pool.Ping(t.Context()); err != nil {
		t.Fatalf("pool did not replace terminated connection: %v", err)
	}
}
