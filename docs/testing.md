# Testing

## For users: `rateltest`

The query builder is generic-method based and cannot be hidden behind an
interface, so ratel offers two test seams instead of mocks of itself:

1. **Ports.** Application code that needs a unit test depends on its own narrow
   non-generic interface (`UserStore.ByEmail`), implemented with ratel and
   mocked with a generated mock. Use cases are tested against the mock;
   the implementation is tested against a real database.
2. **Real database.** `rateltest` makes real PostgreSQL fast enough for every
   test.

| Helper | Does |
|---|---|
| `rateltest.NewServer(t, options...)` | starts a suite-owned testcontainers PostgreSQL and applies expand, backfill and contract SQL to a template database |
| `server.Postgres(t)` | returns a fresh `*pgxpool.Pool` for a database cloned with `CREATE DATABASE ... TEMPLATE`; closes the pool and drops the database in `t.Cleanup` |
| `rateltest.Postgres(t, options...)` | convenience form owning a private server for one test |
| `rateltest.RollbackTx(t, pool, timeout...)` | a transaction rolled back at cleanup, for tests whose code opens no transactions of its own |
| `rateltest.Recorder` | a pgx query, batch and COPY tracer that records attempted SQL and arguments, for query-count assertions |
| `rateltest.FailWith(code).Wrap(executor, match)` | injects `*pgconn.PgError` for matching statements without contacting the executor |
| `rateltest.NewExecutor(t)` | constructs a scripted `*rateltest.Executor` backed by `pgxmock/v5`, with cleanup assertions |

Per-test databases from a template are the default; rollback isolation is
offered only where the code under test does not manage transactions. Container
ownership is explicit rather than process-global: create one server in a parent
test, then call `server.Postgres(t)` in parallel subtests. Parent cleanup runs
after all children, including parallel children. This shares one container per
suite without global state; separate top-level suites own separate containers.

### PostgreSQL configuration and schema

`WithImage` overrides `RATEL_PG_IMAGE`; without either, the image is
`postgres:18-alpine`. Use PostgreSQL 16, 17 or 18. `WithTimeout` bounds startup,
database setup and each cleanup independently; its default is five minutes.
`RollbackTx` accepts one optional positive timeout, also defaulting to five
minutes. Cleanup uses a fresh context, not the test's cancelled context.

`WithMigrations(expand, backfill, contract []string)` copies and applies trusted
SQL in phase order. Each string may contain multiple SQL commands. The helper
executes supplied SQL, not the CLI migration planner or its journal. Use expand
alone for a schema with no data migration. Repeated migration options append
phases in option order. Migration failures fail setup before any child database
is returned. Each clone contains the same migrated rows; parallel tests can
reuse identical primary keys without sharing mutations.

`WithMigrationsDir(migrations fs.FS, options ...migrate.Option)` applies a
migrations directory to the template with `migrate.Apply` and
`migrate.Contract`: every phase, under the same guards and journal as an
application's run, so tests get the schema production gets. Pass the
directory the application embeds. `WithMigrations` and `WithMigrationsDir`
apply in option order.

`WithPoolConfig(func(*pgxpool.Config))` configures each isolated pool, after
schema application. Callbacks compose in option order and must support
concurrent setup. Set `AfterConnect` to generated `RegisterTypes`, or explicitly
load custom codecs and call `ratel.RegisterValue` on every connection. No Go type
mapping is guessed or registered globally. The template and administrative
connections do not run application hooks.

```go
func TestUserStore(t *testing.T) {
    t.Parallel()
    server := rateltest.NewServer(t,
        rateltest.WithMigrations([]string{
            `CREATE SCHEMA app; CREATE TABLE app.users (id bigint PRIMARY KEY, email text NOT NULL)`,
        }, nil, nil),
        rateltest.WithPoolConfig(func(config *pgxpool.Config) {
            config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
                return ratel.RegisterValue[string](conn.TypeMap(), "text")
            }
        }),
    )
    for _, name := range []string{"first", "second"} {
        t.Run(name, func(t *testing.T) {
            t.Parallel()
            pool := server.Postgres(t)
            _, err := pool.Exec(t.Context(),
                `INSERT INTO app.users VALUES (1, $1)`, ratel.NullFrom(name))
            if err != nil {
                t.Fatal(err)
            }
        })
    }
}
```

Always release acquired connections and close cursors before test cleanup.
`pgxpool.Close` waits for borrowed connections. Database cleanup uses
`DROP DATABASE ... WITH (FORCE)` to close other connections to the fixture.
Do not commit a `RollbackTx` transaction: cleanup tolerates an already closed
transaction, but cannot undo a commit. Use a cloned database for code that
commits, begins transactions or needs multiple independent connections.

### Recorder and failure injection

Assign `&rateltest.Recorder{}` to `config.ConnConfig.Tracer` before creating a
pool. `Statements()` returns `[]ratel.Statement` in trace-start order, with
independent statement and argument slices. Argument values are shallow copies;
mutable values keep pgx's caller-owned lifetime contract. `Reset()` clears the
history. Both methods are concurrency-safe. Avoid sharing a recorder between
parallel tests when asserting a per-test query count.

Queries are counted at start, including failures. Each queued batch statement
is counted once, even if the batch later fails; batch callbacks do not count it
again. COPY contributes one `COPY ... FROM STDIN` entry without consuming or
recording its row source. Registration queries and setup queries traced through
the configured pool also appear; reset after setup or filter by SQL. Recorded
SQL and arguments may contain secrets and must not be logged or published.

`FailWith` accepts a five-character uppercase ASCII SQLSTATE; invalid fixture
codes panic. `Wrap` takes a `ratel.Executor` and `func(sql string) bool` selector.
Nonmatching operations delegate, preserving error causes. Matching operations
get fresh `*pgconn.PgError` values. QueryRow fails on Scan; COPY selectors receive
the sanitized table name, not SQL. A batch with any matching statement fails as
a whole before being sent, including Close. This is deterministic failure
injection, not a simulation of partially executed server batches.

The scripted executor embeds pgxmock's pool API: use `ExpectQuery`, `ExpectExec`,
`ExpectBatch`, `ExpectCopyFrom`, `pgxmock.NewRows` and `pgxmock.NewResult`.
Matching is exact after whitespace normalization; expectations are ordered by
default. Unexpected calls return pgxmock errors; missing expectations fail
cleanup. For PostgreSQL codecs, SQL semantics and transaction behavior, use the
real database rather than the fake.

Run helper unit tests and benchmarks with:

```sh
go -C rateltest test -race ./...
go -C rateltest test -run '^$' -bench . -benchmem ./...
go -C rateltest test -tags integration -run '^$' -bench 'Benchmark(NewServer|Postgres)$' -benchtime=1x -benchmem
```

Lifecycle benchmarks run fixture cleanup inside each iteration, so databases,
transactions and cleanup callbacks do not accumulate. The server benchmark
includes container startup and teardown; the clone benchmark reuses one server.

The container tests live beside the helpers under the `integration` build tag.
`make test-integration` runs them and the suite in `tests/`. Run the PostgreSQL
matrix with `RATEL_PG_IMAGE=postgres:16-alpine make test-integration`, then repeat
with `postgres:17-alpine` and `postgres:18-alpine`. Helper integration covers
phase ordering, parallel database isolation, pool type registration, rollback,
real pgx tracing and recovery after a terminated connection.

## For ratel itself

Gates, in order; a gate starts only after the previous one passes:

1. **Unit tests** next to every file, table-driven and parallel, mocks
   generated. Parser, catalog, inference, diff, assessment, planner, lint and
   plugin host are pure and tested without a database.
2. **Benchmarks** next to every function with logic; hot paths (scanning,
   builder rendering, loader distribution, catalog build, diff) carry
   before/after numbers when changed.
3. **Linter**: golangci-lint with the repository config, no suppressions.
4. **Integration** in `tests/`, one module against real infrastructure in
   testcontainers, on PostgreSQL 16, 17 and 18:
   - catalog round trip (`build` equals `introspect(apply)`), per fixture;
   - the lock and effect table replayed against `pg_locks` and `relfilenode`;
   - every safe recipe applied under a concurrent long transaction and a
     concurrent write load, asserting that no reader or writer waits longer
     than `lock_timeout`;
   - **chaos**: connection killed mid-step, server restart, `55P03` storms,
     statement timeout during a backfill; the next run resumes from the
     journal and the end state equals the planned state;
   - relation loading strategies return identical graphs;
   - plugin conformance suite against `ratel-gen-go`
     (`cmd/ratel/internal/gengo/conformance_test.go`, through the same encoded
     exchange an executable serves);
   - generated code (`cmd/ratel/tests/gengo_test.go`): `ratel generate` writes a
     fixture's packages into a temporary module, which builds through a
     temporary `go.work` with the repository and runs a program against real
     PostgreSQL: type registration, inserts, reads, relations, static queries,
     `COPY`, outer-join rows and constraint sentinels.
5. **End to end**: a sample service generated by ratel, migrated, then
   exercised through its public API on testcontainers; extended as each module
   lands.

### Comparison with other libraries

Module `bench` runs one schema and seed (1000 users, 10 orders each) through
pgx by hand, ratel, sqlc, GORM, bun and ent: a row by key, a page of 100, the
page with its orders, an insert, a bulk insert of 100 and an update by key.
Each library is configured as its documentation advises for speed and uses
its own idiom (sqlc's and pgx's bulk insert is `COPY`); the database/sql
libraries run on pgx's stdlib driver over the same pool. PostgreSQL runs with
`fsync`, `synchronous_commit`, `full_page_writes` and autovacuum off and every
library is warmed up first, so the numbers measure the libraries, not the
disk. `TestLibrariesAgree` (in `make test-integration`) checks that all six
read and write the same data. `make bench-gen` regenerates the ratel, sqlc
(Docker image `sqlc/sqlc`) and ent code; `make bench-compare` runs
`BENCH_COUNT` (default 10) repetitions into `bench/results.txt` and tabulates
them with benchstat into `bench/results-table.txt`.

### Output-plugin SDK and host

The public conformance helpers live in `plugin/plugintest`. Authors run
`plugintest.Run(t, plugintest.Command(binary, timeout), cases...)` from their own
tests. Optional cases pair Generate requests with golden PluginResponses;
expected error diagnostics are checked like successful output. A custom
`plugintest.Exchange` adapts another transport.

SDK, configuration, catalog ownership and CLI unit tests have no external
infrastructure. `cmd/ratel/tests/plugin` builds a real native plugin and a real
WASI preview-1 plugin from `testdata/generator`. These integration tests need the
Go toolchain, not Docker, and run without a build tag. They verify checksum
rejection even with cached Info, execution deadlines, failure recovery, safe
output paths, executable conformance and dump-request replay.

```sh
go test -race ./plugin/...
go -C cmd/ratel test -race ./internal/plugin/... ./internal/config/... ./internal/cli/...
go -C cmd/ratel test -race ./tests/plugin/...
go test -run '^$' -bench . -benchmem ./plugin/...
go -C cmd/ratel test -run '^$' -bench . -benchmem ./internal/plugin/... ./internal/config/... ./internal/cli/...
go -C cmd/ratel test -run '^$' -bench BenchmarkTransports -benchtime=1x -benchmem ./tests/plugin/...
```

Property tests cover order independence of the catalog build and determinism
of the IR and of plugin output.
