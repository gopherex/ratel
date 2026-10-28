# CLI and configuration

One binary, `ratel`, installed with
`go install github.com/gopherex/ratel/cmd/ratel@latest`.

## Commands

| Command | Does | Needs a database |
|---|---|---|
| `ratel init` | writes a commented `ratel.yaml` and the source directories | no |
| `ratel check` | parses all sources, builds the Catalog, infers queries, validates annotations; prints diagnostics | no |
| `ratel generate` | `check`, then runs every configured plugin and writes their files | no |
| `ratel dump [--format binpb\|json] [--plugin <name>]` | writes the Catalog, or the exact request for one plugin | no |
| `ratel migrate plan <name>` | diffs the schema files against the state the existing migrations produce, then writes new planned steps | dev database |
| `ratel migrate lint [<file>...]` | runs the `RTL-*` rules over planned steps and hand-written migrations | dev database for rewrite probes |
| `ratel migrate verify` | checks `ratel.sum` against the migrations directory | no |
| `ratel migrate status --url <dsn>` | journal state: applied, pending, interrupted steps, current phase | target |
| `ratel migrate apply --url <dsn>` | runs pending steps of the `expand` and `backfill` phases | target |
| `ratel migrate contract --url <dsn>` | runs pending steps of the `contract` phase | target |
| `ratel migrate drift --url <dsn>` | introspects the target and diffs it against the state the applied migrations should have produced | target and dev database |

Exit codes: `0` success; `1` diagnostics with severity `ERROR`, a failed
migration step, a checksum mismatch or detected drift; `2` usage error.
Diagnostics print as `file:line:column: severity CODE: message`; `--format=json`
prints them as JSON lines.

The dev database is a disposable PostgreSQL used to let PostgreSQL itself
normalize both sides of a diff and to run rewrite probes. It is a testcontainers
container from `dev.image` or an existing server from `dev.url`. ratel creates
and drops its own databases on it (`CREATE DATABASE ... TEMPLATE template0`),
so one server serves both sides of a diff.

## `ratel.yaml`

```yaml
version: 1

postgres:
  version: 18                 # production major version: inference and recipes target it

parser:
  concurrency: 1              # shared bound for WASM parse, scan and deparse operations

schema:  [schema/**/*.sql]
queries:                      # query files grouped into Go packages
  - files: [queries/users/*.sql]
    package: users
  - files: [queries/billing/*.sql]
    package: billing
migrations: migrations

dev:
  image: postgres:18-alpine   # or: url: postgres://...

plugins:
  - name: go
    builtin: true
    out: internal/db
    options:
      package: db

migrate:
  lock_timeout: 100ms         # first attempt; the retry schedule grows it
  retry_schedule: gitlab      # the GitLab with_lock_retries table without its final no-timeout attempt
  max_total_wait: 10m         # exhausted: the step fails and prints blocking sessions
  statement_timeout: 5s       # short DDL steps
  long_tx_threshold: 1m       # pre-flight: transactions older than this block lock-taking steps
  backfill:
    batch: 1000
    pause: 50ms
    max_replica_lag: 10s
```

Rules:

- Unknown keys are errors. Every duration and size is a value in this file;
  the code holds no tuning constants besides these defaults.
- `parser.concurrency` defaults to `1`; omitted or zero selects this default,
  and negative values are errors. One `parse.Parser` is constructed from the
  parser configuration before starting concurrent work and injected into every
  catalog, mapper and annotation operation. All three WASM operations (parse,
  scan and deparse) share its semaphore. Do not construct a parser per request:
  the upstream pool retains every instance and its grown linear memory.
- Plugin `options` is passed to the plugin as bytes of its own options
  message; the plugin validates it in `Info` and `Generate`.
- `postgres.version` must be between 16 and the latest major this ratel
  version supports. A planned step records it; `migrate apply` refuses a
  server whose major is lower than the step's or higher than the latest
  supported one.
