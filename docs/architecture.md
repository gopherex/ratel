# Architecture

ratel is a PostgreSQL data-access ecosystem for Go: sqlc-style code generation
from `.sql` queries, a code-generated ORM and typed query builder built on Go
1.27 generic methods, and a declarative migration engine whose plans never lock
a production database. This document is the target state; the other documents
in `docs/` specify each part.

| Document | Covers |
|---|---|
| [input.md](input.md) | Schema, query and migration sources; the annotation grammar |
| [ir.md](ir.md) | The Catalog IR and its invariants |
| [plugins.md](plugins.md) | The output-plugin protocol |
| [cli.md](cli.md) | The `ratel` command and `ratel.yaml` |
| [runtime.md](runtime.md) | The Go runtime library and the generated Go code |
| [migrations.md](migrations.md) | Diff, plan, lint, apply; migration safety |
| [proto-frontend.md](proto-frontend.md) | `protoc-gen-ratel`: tables declared in `.proto` |
| [testing.md](testing.md) | `rateltest` for users; how ratel itself is tested |

## Scope

- PostgreSQL only, versions 16 to the latest release (18 today). The IR models
  PostgreSQL faithfully; it is not a dialect-neutral abstraction.
- Go 1.27 or newer for the runtime and the generated code.
- One driver: `github.com/jackc/pgx/v5`, native API. `database/sql` is not
  supported: it has no batch, no COPY, no LISTEN/NOTIFY and no codec
  registration, and it is slower on every read path
  (measured: `.claude/out/research-driver-bench.md` at design time).

## Pipeline

```
 ┌──────────────────────── sources (all plain SQL) ───────────────────────┐
 │ schema/*.sql  (hand-written or emitted by protoc-gen-ratel, any number) │
 │ queries/*.sql (named static queries)                                    │
 │ migrations/   (planned steps, checksummed)                              │
 └──────────────────────────────────┬──────────────────────────────────────┘
                                    │ parse (libpg_query, wasm, no cgo)
                                    ▼
             catalog build (order-independent) ─► query inference ─► relations
                                    │
                                    ▼
                           Catalog IR (protobuf)
              ┌─────────────────────┼──────────────────────────┐
              ▼                     ▼                          ▼
     output plugins          migration engine            ratel dump
  (ratel-gen-go first,   (introspect live DB, diff,     (IR as binpb/json
   any language, stdio    assess safety, plan, lint,     for plugin authors)
   or wasm)               apply with guards)
```

There is exactly one input format: PostgreSQL SQL. A frontend that starts from
another language (proto today) emits DDL with ratel annotations, and ratel reads
it like hand-written SQL. There is no input-plugin protocol. Reasons: one
parser, one validation path, one object-id convention, merging reduces to
reading a set of files, and the migration engine compares exactly what
PostgreSQL sees.

## Components

| Component | Role | Origin |
|---|---|---|
| parse | libpg_query AST, scanning and deparsing via one injected, concurrency-bounded WASM parser | sqld `internal/parse`, bounded wrapper |
| mapper | AST to IR messages, deterministic `node_id` | sqld `internal/mapper`, extended |
| catalog | Builds one Catalog from a set of DDL files, order-independent | sqld `internal/catalog`, reworked |
| query | Parameter and column inference with nullability | sqld `internal/query`, extended |
| relations | Relation graph from FKs plus annotations | new design |
| introspect | Live database to the same Catalog shape | sqld `internal/introspect`, extended |
| diff | Changes between two PostgreSQL-normalized renderings | sqld `internal/diff`, reworked |
| safety | `Assess(change, env)`: locks, effect, tx mode, destruction, compatibility | new |
| planner | Rewrites changes into safe steps and phases | new |
| lint | `RTL-*` rules over planned steps and hand-written SQL | new rule set |
| authoring | Writes, classifies and seals migration files; applies them to dev databases | new |
| migrate | Public step runner with journal and lock guards, for the CLI and applications | sqld `pkg/migrate`, reworked |
| plugin host | Runs output plugins (exec or wazero) | rewritten |
| ratel-gen-go | Go output plugin: models, columns, scanners, relations, static queries | new |
| runtime | `ratel` Go package: query builder, loader, executor port | new |
| rateltest | Test helpers for users | new |
| protoc-gen-ratel | `.proto` with ratel options to DDL with annotations | new |

## Boundaries

- The IR is the only contract between the host and output plugins. A plugin
  never sees raw SQL ASTs from libpg_query or Go types of the host.
- The runtime library never parses SQL and never imports the host. Generated
  code depends only on the runtime library, pgx and the standard library.
- The runtime library owns no connections and no transactions. It runs over a
  caller-provided executor (a pool, a connection, a `pgx.Tx`, or a
  context-routed executor such as `github.com/gopherex/pgtx`).
- Generated SQL is always schema-qualified. Nothing depends on `search_path`.

## Modules

| Module | Contains | Depends on |
|---|---|---|
| `github.com/gopherex/ratel` | runtime package `ratel`, migration runner `migrate`, Go types of the IR and plugin protos, plugin SDK | pgx v5, protobuf runtime |
| `github.com/gopherex/ratel/rateltest` | user test helpers | the runtime module, testcontainers |
| `github.com/gopherex/ratel/cmd/ratel` | host CLI, parser, catalog, inference, migration engine, `ratel-gen-go` linked in | the runtime module, wazero, go-pgquery, testcontainers |
| `github.com/gopherex/ratel/cmd/protoc-gen-ratel` | proto frontend | protobuf, the options proto |

The split keeps parser, wasm runtime and testcontainers out of the dependency
graph of applications that only import the runtime. A root `go.work` ties the
modules together for development; released modules require released versions
of each other (no `replace`).

`ratel-gen-go` is an ordinary output plugin that speaks the protocol of
[plugins.md](plugins.md). The CLI links it in and calls it in-process through
the same request and response messages, so the built-in generator cannot use
anything a third-party plugin cannot.

## Repository layout

```
proto/ratel/v1/{ir,plugin,options}/*.proto   contract source (easyp, see below)
ir/                                          generated Go for the IR protos
options/                                     generated Go for the ratel.proto options
plugin/pluginpb/                             generated Go for the plugin protocol
plugin/                                      Go SDK for output plugins
ratel.go, query.go, col.go, rel.go, ...      runtime package ratel
migrate/                                     migration runner for the CLI and applications
rateltest/                                   user test helpers (own module)
cmd/ratel/                                   host CLI module
cmd/protoc-gen-ratel/                        proto frontend module
internal/                                    host internals (inside cmd/ratel)
tests/                                       integration and e2e suites
docs/                                        this specification
website/                                     Docusaurus documentation site
```

## Proto toolchain

- Contract files are proto3 with `optional` for presence. Editions are not used:
  the wire format is identical, so a later move is a source change only.
- Generation runs with easyp v1 (`easyp.yaml`, `easyp.gen.yaml`,
  `protobuf.mod`, `protobuf.lock`). Plugins `protoc-gen-go` resolve from
  `PATH`. Generated Go is committed.
- `easyp lint` and `easyp breaking` run in CI from the first tagged release.
  Field numbers are never reused; removed fields are `reserved`.
