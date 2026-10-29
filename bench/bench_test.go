//go:build integration

// Package bench compares ratel with pgx, sqlc, GORM, bun and ent on the same
// PostgreSQL schema, data and operations. Run it with make bench-compare.
package bench_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Data the benchmarks read.
const (
	seedUsers      = 1000
	ordersPerUser  = 10
	pageSize       = 100
	containerWait  = 5 * time.Minute
	defaultImage   = "postgres:18-alpine"
	readyMessages  = 2
	pageStartAfter = 400 // id after which the list scenario reads a page
)

// newUser is a row an insert scenario writes.
type newUser struct{ email, name string }

// library runs every scenario with one data-access library. Each scenario
// returns what it read, so the libraries can be checked to agree.
type library struct {
	name string
	// one reads a user by primary key and returns its email.
	one func(ctx context.Context, id int64) (string, error)
	// list reads a page of users ordered by id after an id and returns the
	// emails.
	list func(ctx context.Context, after int64) ([]string, error)
	// insert writes one user.
	insert func(ctx context.Context, user newUser) error
	// insertBatch writes users in one call, the library's bulk insert.
	insertBatch func(ctx context.Context, users []newUser) error
	// relations reads a page of users with their orders and returns the
	// number of orders loaded.
	relations func(ctx context.Context) (int, error)
	// update renames a user by primary key.
	update func(ctx context.Context, id int64, name string) error
}

// connections are the handles the libraries run on: pgx natively, the
// database/sql libraries through pgx's stdlib driver over the same pool.
type connections struct {
	pool *pgxpool.Pool
	db   *sql.DB
}

func libraries(tb testing.TB, conn connections) []library {
	tb.Helper()

	return []library{
		pgxLibrary(conn), ratelLibrary(conn), sqlcLibrary(conn),
		gormLibrary(tb, conn), bunLibrary(conn), entLibrary(conn),
	}
}

// server starts PostgreSQL and loads the schema and seed data.
func server(tb testing.TB) connections {
	tb.Helper()

	image := os.Getenv("RATEL_PG_IMAGE")
	if image == "" {
		image = defaultImage
	}

	ctx, cancel := context.WithTimeout(context.Background(), containerWait)
	defer cancel()

	// Durability is off: writes then measure the libraries and the protocol,
	// not the disk's flush latency, which would drown their differences.
	container, err := postgres.Run(ctx, image,
		testcontainers.WithCmdArgs("-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
			"-c", "autovacuum=off"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(readyMessages).WithStartupTimeout(containerWait)),
	)
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

	pool, err := pgxpool.New(ctx, container.MustConnectionString(ctx, "sslmode=disable"))
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(pool.Close)

	schema, err := os.ReadFile("schema/schema.sql")
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		tb.Fatal(err)
	}

	seed(ctx, tb, pool)

	database := stdlib.OpenDBFromPool(pool)

	tb.Cleanup(func() { _ = database.Close() })

	return connections{pool: pool, db: database}
}

func seed(ctx context.Context, tb testing.TB, pool *pgxpool.Pool) {
	tb.Helper()

	users := make([][]any, 0, seedUsers)
	orders := make([][]any, 0, seedUsers*ordersPerUser)

	for id := int64(1); id <= seedUsers; id++ {
		users = append(users, []any{id, "user" + strconv.FormatInt(id, 10) + "@example.org", "User"})

		for index := range int64(ordersPerUser) {
			orders = append(orders, []any{id, index * 100, "paid"})
		}
	}

	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"users"}, []string{"id", "email", "name"},
		pgx.CopyFromRows(users)); err != nil {
		tb.Fatal(err)
	}

	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"orders"}, []string{"user_id", "total", "status"},
		pgx.CopyFromRows(orders)); err != nil {
		tb.Fatal(err)
	}

	// The seed set explicit ids; inserts continue after them.
	_, err := pool.Exec(ctx, `SELECT setval(pg_get_serial_sequence('users', 'id'), max(id)) FROM users`)
	if err != nil {
		tb.Fatal(err)
	}

	// Autovacuum is off: the tables are vacuumed and analyzed once, so no
	// background work lands in a measurement.
	if _, err := pool.Exec(ctx, `VACUUM ANALYZE users`); err != nil {
		tb.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `VACUUM ANALYZE orders`); err != nil {
		tb.Fatal(err)
	}
}

// warmUps is how often warm runs every scenario of every library.
const warmUps = 200

// warm runs every read scenario of every library before any measurement,
// so the first library measured does not pay for cold caches, connections
// and statement preparation.
func warm(b *testing.B, libs []library) {
	b.Helper()

	for range warmUps {
		for _, lib := range libs {
			if _, err := lib.one(b.Context(), 1); err != nil {
				b.Fatal(err)
			}

			if _, err := lib.list(b.Context(), pageStartAfter); err != nil {
				b.Fatal(err)
			}

			if _, err := lib.relations(b.Context()); err != nil {
				b.Fatal(err)
			}

			if err := lib.update(b.Context(), 1, "Warm"); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// batch is pageSize users with emails unique to the prefix and index.
func batch(prefix string, index int) []newUser {
	users := make([]newUser, pageSize)
	for row := range users {
		users[row] = newUser{email: fmt.Sprintf("%s-%d-%d@example.org", prefix, index, row), name: "Batch"}
	}

	return users
}

// TestLibrariesAgree runs every scenario once per library and checks that
// they read and write the same data, so the benchmarks compare equal work.
func TestLibrariesAgree(t *testing.T) {
	t.Parallel()

	conn := server(t)

	for index, lib := range libraries(t, conn) {
		t.Run(lib.name, func(t *testing.T) {
			t.Parallel()

			agreeReads(t, lib)
			// Each library renames a user of its own; the seed has ids 1 to seedUsers.
			agreeWrites(t, conn, lib, int64(seedUsers-index))
		})
	}
}

func agreeReads(t *testing.T, lib library) {
	t.Helper()

	ctx := t.Context()

	if email, err := lib.one(ctx, 7); err != nil || email != "user7@example.org" {
		t.Errorf("one = %q, %v", email, err)
	}

	emails, err := lib.list(ctx, pageStartAfter)
	if err != nil || len(emails) != pageSize || emails[0] != "user401@example.org" {
		t.Errorf("list = %d emails from %v, %v", len(emails), first(emails), err)
	}

	if orders, err := lib.relations(ctx); err != nil || orders != pageSize*ordersPerUser {
		t.Errorf("relations = %d orders, %v", orders, err)
	}
}

func agreeWrites(t *testing.T, conn connections, lib library, renamed int64) {
	t.Helper()

	ctx := t.Context()

	if err := lib.insert(ctx, newUser{email: lib.name + "-agree@example.org", name: "One"}); err != nil {
		t.Errorf("insert: %v", err)
	}

	if err := lib.insertBatch(ctx, batch(lib.name+"-agree", 0)); err != nil {
		t.Errorf("insert batch: %v", err)
	}

	if err := lib.update(ctx, renamed, lib.name); err != nil {
		t.Errorf("update: %v", err)
	}

	var count int

	err := conn.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email LIKE $1`, lib.name+"-agree%").Scan(&count)
	if err != nil || count != 1+pageSize {
		t.Errorf("inserted users = %d, %v", count, err)
	}

	var name string

	err = conn.pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, renamed).Scan(&name)
	if err != nil || name != lib.name {
		t.Errorf("updated name = %q, %v", name, err)
	}
}

func first(emails []string) string {
	if len(emails) == 0 {
		return ""
	}

	return emails[0]
}

// BenchmarkLibraries runs each scenario with each library on the same data:
// BenchmarkLibraries/scenario=<scenario>/library=<library>, the keys
// benchstat projects rows and columns by.
func BenchmarkLibraries(b *testing.B) {
	conn := server(b)
	libs := libraries(b, conn)

	warm(b, libs)

	scenarios := []struct {
		name string
		body func(b *testing.B, lib library)
	}{
		{"one", benchOne},
		{"list", benchList},
		{"relations", benchRelations},
		{"insert", benchInsert},
		{"insert-batch", benchInsertBatch},
		{"update", benchUpdate},
	}

	for _, scenario := range scenarios {
		b.Run("scenario="+scenario.name, func(b *testing.B) {
			for _, lib := range libs {
				b.Run("library="+lib.name, func(b *testing.B) {
					b.ReportAllocs()
					scenario.body(b, lib)
				})
			}
		})
	}
}

// unique is a prefix no other benchmark run used: -count runs every
// sub-benchmark repeatedly on the same database.
func unique(lib library) string {
	return lib.name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func benchOne(b *testing.B, lib library) {
	b.Helper()

	for index := 0; b.Loop(); index++ {
		if _, err := lib.one(b.Context(), int64(index%seedUsers+1)); err != nil {
			b.Fatal(err)
		}
	}
}

func benchList(b *testing.B, lib library) {
	b.Helper()

	for b.Loop() {
		if _, err := lib.list(b.Context(), pageStartAfter); err != nil {
			b.Fatal(err)
		}
	}
}

func benchRelations(b *testing.B, lib library) {
	b.Helper()

	for b.Loop() {
		if _, err := lib.relations(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func benchInsert(b *testing.B, lib library) {
	b.Helper()

	prefix := unique(lib)

	for index := 0; b.Loop(); index++ {
		user := newUser{email: fmt.Sprintf("%s-%d@example.org", prefix, index), name: "One"}
		if err := lib.insert(b.Context(), user); err != nil {
			b.Fatal(err)
		}
	}
}

func benchInsertBatch(b *testing.B, lib library) {
	b.Helper()

	prefix := unique(lib)

	for index := 0; b.Loop(); index++ {
		if err := lib.insertBatch(b.Context(), batch(prefix, index)); err != nil {
			b.Fatal(err)
		}
	}
}

func benchUpdate(b *testing.B, lib library) {
	b.Helper()

	for index := 0; b.Loop(); index++ {
		if err := lib.update(b.Context(), int64(index%seedUsers+1), "Renamed"); err != nil {
			b.Fatal(err)
		}
	}
}
