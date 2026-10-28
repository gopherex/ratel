# ratel

ratel is a PostgreSQL data-access toolkit for Go:

- **code generation** from `.sql` schema and query files, sqlc style: models,
  scanners, typed static queries;
- a **generated ORM and typed query builder** on Go 1.27 generic methods
  (selects, joins, keyset pagination, batched writes, relations);
- **declarative migrations** that never lock a production database: the
  schema files state the target, ratel plans safe steps, lints them and
  applies them under lock guards.

One input format (PostgreSQL SQL), one driver (pgx v5), one contract (the
Catalog IR in protobuf) between the host and its output plugins. The
specification is [docs/](docs/architecture.md); code follows it.

## Status

Pre-release: the runtime, code generation and the migration engine are
implemented and tested against PostgreSQL 16, 17 and 18; `ratel init` and the
documentation site are not yet. The API may still change before the first
tag.

## How it fits together

```
schema/*.sql ──► parse ──► catalog ──► query inference ──► plugins (ratel-gen-go) ──► Go code
queries/*.sql ─┘                │
                                └─► ratel migrate plan:
   migrations/*.sql ─► dev DB ─► render ─┐
   schema/*.sql ─────► dev DB ─► render ─┴─► diff ─► probe ─► plan ─► lint ─► migrations/<ts>_<name>.sql
                                                                              ratel.sum
   ratel migrate apply/contract ─► target DB (journal, advisory lock, lock guards, batched backfill)
```

## Layout

```
*.go                    runtime package ratel (module github.com/gopherex/ratel)
rateltest/              test helpers for applications (own module)
cmd/ratel/              host CLI (own module)
  internal/parse        libpg_query (WASM) parser, one shared bound
  internal/catalog      DDL files to the Catalog IR, order independent
  internal/query        query parameter and column inference
  internal/gengo        ratel-gen-go, the Go output plugin
  internal/introspect   a live database rendered as keyed DDL
  internal/diff         changes between two renderings
  internal/safety       lock and effect table, statement classifier
  internal/plan         safe recipes and phases
  internal/probe        rewrite probes and dependencies on the dev database
  internal/lint         RTL-* rules
  internal/migrate      step files, ratel.sum, dev apply, the runner
  internal/cli          the commands
  tests/                integration suites (build tag `integration`)
cmd/protoc-gen-ratel/   proto frontend: .proto options to schema DDL (own module)
proto/                  contract sources: ratel/v1/{ir,plugin,options}/*.proto
ir/, plugin/, options/  generated Go of the contracts, and the plugin SDK
tests/                  catalog integration suite (own module)
docs/                   the specification
```

## Quick start

`ratel.yaml` next to the sources ([docs/cli.md](docs/cli.md) lists every key):

```yaml
version: 1
postgres: {version: 18}
schema: [schema/**/*.sql]
queries:
  - files: [queries/users/*.sql]
    package: users
migrations: migrations
dev: {image: postgres:18-alpine}
plugins:
  - name: go
    builtin: true
    out: internal/db
    options: {import_path: github.com/acme/app/internal/db}
```

```sh
go install github.com/gopherex/ratel/cmd/ratel@latest

ratel check                          # parse, build the catalog, infer queries
ratel generate                       # write the generated Go code
ratel migrate plan add_orders        # plan a migration from the schema files
ratel migrate lint                   # RTL-* rules over every migration
ratel migrate apply --url $DATABASE_URL     # expand and backfill phases
ratel migrate contract --url $DATABASE_URL  # contract, once old code is gone
ratel migrate drift --url $DATABASE_URL     # compare the target with its migrations
```

Schema annotations steer the migration engine: `@ratel.renamed from=old`,
`@ratel.retype using=<expr>`, `@ratel.backfill expr=<expr>` and
`@ratel.allow <RTL-code> reason=<text>` ([docs/input.md](docs/input.md),
[docs/migrations.md](docs/migrations.md)).

## Requirements

- Go 1.27.3 or newer.
- Docker, for the integration suites and the default dev database
  (testcontainers-go); `dev.url` points at an existing server instead.
- For `make proto`: easyp built from a local clone (`make setup`) and
  `protoc-gen-go` on PATH.

## Commands

```sh
make setup             # install pinned golangci-lint and easyp into ./bin
make check             # format check + linter + unit tests (what CI runs)
make test-integration  # integration suites on Docker (postgres:18-alpine)
```

Useful extras: `make lint`, `make fmt` (never `golangci-lint fmt` at the
root: it rewrites the nested modules), `make test`, `make bench`,
`make proto`. `RATEL_PG_IMAGE` overrides the integration image, e.g.
`RATEL_PG_IMAGE=postgres:17-alpine make test-integration`.

## Tests

- Unit tests sit next to every file, table-driven and parallel, with a
  benchmark for every function with logic (`make bench`).
- Integration tests run against real PostgreSQL 16, 17 and 18 in
  testcontainers: introspection round trips, the diff and every plan applied
  and rendered back, the lock and effect table replayed and compared with
  `pg_locks`, the generated code compiled and run, and the whole
  `ratel migrate` cycle.
- Failure tests: lost connections during introspection, steps blocked by
  other sessions until `max_total_wait`, failed concurrent index builds
  cleaned up and resumed, interrupted backfills resumed from their key, a
  second concurrent run refused.

Generated Go is committed; `make proto` regenerates it when the contracts
change.
