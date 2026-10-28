# AGENTS.md

ratel: PostgreSQL data access for Go (codegen, ORM, query builder, safe
declarative migrations). The specification is `docs/`, starting at
[docs/architecture.md](docs/architecture.md). Code follows the spec; a
divergence is a code bug or a spec change made in the same commit.

## Toolchain

- Go 1.27.3 or newer (generic methods). gopls v0.24.0 or newer understands them.
- PostgreSQL 16 to 18 for integration tests (testcontainers, Docker).
- Proto: easyp v1 built from source into `./bin/easyp` (gitignored); plugins
  such as `protoc-gen-go` from `PATH`; generated Go is committed.

## Task -> tool

| Task | Tool |
|---|---|
| Lint | golangci-lint, pinned, via `make lint` (config `.golangci.yaml`) |
| Proto generation | easyp v1 (`make proto`; built from the local clone into `./bin`) |
| PostgreSQL in tests | testcontainers-go, postgres module |
| Driver | pgx v5, native API (`database/sql` is not supported) |
| Transactions | `github.com/gopherex/pgtx` >= v1.1.4 |

## Rules specific to this repo

- The runtime package `ratel` and generated code never import `reflect`.
- Generated SQL is always schema-qualified.
- Every tuning value (timeouts, batch sizes, retry schedule) is configuration,
  read from `ratel.yaml`; code holds only documented defaults.
