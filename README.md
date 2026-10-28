# ratel

ratel is a PostgreSQL data-access ecosystem for Go: sqlc-style code generation
from `.sql` queries, a generated ORM and typed query builder built on Go 1.27
generic methods, and a declarative migration engine whose plans never lock a
production database. One input format (PostgreSQL SQL), one driver (pgx v5),
one contract (the Catalog IR in protobuf) between the host and output plugins.

## Status

Pre-release. The specification in [docs/](docs/architecture.md) is the target
state; code follows it and the first components are still landing.

## Layout

```
doc.go                  runtime package ratel (module github.com/gopherex/ratel)
rateltest/              user test helpers (own module)
cmd/ratel/              host CLI (own module; only `version` exists today)
cmd/protoc-gen-ratel/   proto frontend plugin: .proto options to schema DDL (own module)
proto/                  contract sources: ratel/v1/{ir,plugin,options}/*.proto
                        plus the easyp v1 config (protobuf.mod, easyp.yaml, ...)
tests/                  integration suite (own module, build tag `integration`)
docs/                   the specification
```

## Requirements

- Go 1.27.3 or newer.
- Docker, for the integration suite (testcontainers-go).
- For `make proto`: easyp built from a local clone (`make setup`) and
  `protoc-gen-go` on PATH. Not needed until `.proto` files exist.

## Commands

```sh
make setup             # install pinned golangci-lint and easyp into ./bin
make check             # format check + linter + unit tests (what CI runs)
make test-integration  # integration suite on Docker (postgres:18-alpine)
```

Useful extras: `make lint`, `make fmt`, `make test`, `make bench`, `make proto`.
`RATEL_PG_IMAGE` overrides the integration image, e.g.
`RATEL_PG_IMAGE=postgres:17-alpine make test-integration`.

Generated Go is committed; `make proto` regenerates it when the contracts
change.
