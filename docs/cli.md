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
| `ratel migrate new <name>` | writes an empty hand-written migration file | no |
| `ratel migrate hash` | seals hand-written steps with their classification and updates `ratel.sum` | no |
| `ratel migrate lint [<file>...]` | runs the `RTL-*` rules over planned steps and hand-written migrations | dev database for rewrite probes |
| `ratel migrate verify` | checks `ratel.sum` against the migrations directory and that every step is sealed | no |
| `ratel migrate status --url <dsn>` | journal state: applied, pending, interrupted steps, current phase | target |
| `ratel migrate apply --url <dsn>` | runs pending steps of the `expand` and `backfill` phases | target |
| `ratel migrate contract --url <dsn>` | runs pending steps of the `contract` phase | target |
| `ratel migrate drift --url <dsn>` | introspects the target and diffs it against the state the applied migrations should have produced | target and dev database |

### Check and generate

`ratel check [--config <path>]` builds the catalog, infers views and queries,
and prints warnings; a failure prints the error and exits `1`.
`ratel generate [--config <path>]` does the same, then runs every configured
plugin in order and writes its files under its `out`, relative to the
configuration file; the request's `out_dir` is that absolute directory. Files
are replaced atomically (a temporary file renamed over the target).
`out/.ratel-manifest` lists the files the plugin wrote, one slash-separated
path per line; the next run deletes listed files that are absent now, and
directories they leave empty, and never touches files the manifest does not
list. A path through a symbolic link, inside the output directory or as the
directory itself, is refused before anything is written.

### Init

`ratel init [--dir <path>]` writes a commented `ratel.yaml` with the default
values, creates `schema/` (with an empty `schema.sql`, so the schema pattern
matches a file), `queries/` and `migrations/`. With a `go.mod` in the
directory it configures the Go generator with `import_path`
`<module>/internal/db`; without one the generator is a commented example.
An existing `ratel.yaml` is never overwritten: `init` fails instead.

### Migrate

Every `migrate` subcommand reads `ratel.yaml` (`--config <path>`) and the
migrations directory, created when missing.

- `plan <name>` first runs `verify`, then on the dev server applies the
  existing migration files to one database and the schema files to another
  (in any order: foreign keys go last, other statements retry until their
  dependencies exist), renders both over the desired schemas and those the
  migrations created, diffs, probes and plans. It prints every finding as
  `<where> fixed|allowed|error <code>: <detail>`; with an `error` finding it
  writes nothing and exits `1`. Without changes it prints `no changes`.
  Otherwise it writes `<UTC timestamp>_<name>.sql` and the new `ratel.sum`.
- `new <name>` writes `<UTC timestamp>_<name>.sql` holding a comment that
  explains the hand-written format, records it in `ratel.sum` and prints
  `wrote <file>`.
- `hash` seals every hand-written step (see
  [migrations.md](migrations.md#planned-files)), prints `sealed <file>` per
  rewritten file and writes `ratel.sum`.
- `lint [<file>...]` applies the files one by one to a dev database and
  lints each named file (all when none is named) against the state before
  it, then the desired schema.
- `verify` runs `migrate.Check`: the files match `ratel.sum` and every step
  is sealed; a directory without files needs no `ratel.sum`.
- `status`, `apply`, `contract` and `drift` take `--url`. `status`, `apply`
  and `contract` call `migrate.Status`, `migrate.Apply` and
  `migrate.Contract` with the `migrate` settings; `apply` and `contract`
  print `msg="migration step" phase=<phase> class=<class> file=<file>
  step=<id>` per step started. `status` prints
  `pending|applied|interrupted|changed <phase> <file> <step>` per step.
  `drift` prints each difference as `<change kind> <key>` and exits `1` when
  there is one.

### Dump

Every command of the table is implemented. `dump` reads `ratel.yaml` from the
working directory, or the file selected by `--config <path>`. Source patterns
and plugin paths are relative to that configuration file, not the working
directory. Schema patterns support `**`, expand in sorted order and deduplicate
files; a pattern matching no files is an error. An explicit `schema: []` builds
an empty catalog.

`dump` builds the schema Catalog, including validated core annotations and parsed
plugin annotations, using the configured PostgreSQL built-in snapshot, then infers
every configured query (`annotate.Queries`, `query.Infer`) with the same parser and
catalog. Query files of each group are read once; a file matched by two groups is an
error. Plain dump serializes the Catalog only. `--plugin` requests carry the inferred
queries, and each query gets a plugin-namespace annotation `package <group>` (target
`query_name`) so the plugin knows its configured Go package. Warnings from query
annotation go to stderr.
Plain dump serializes `Catalog`; `--plugin <name>` negotiates Info and serializes
a `PluginRequest` with its Generate payload, filtered namespace annotations,
JSON options, host version and configured output directory. It does not invoke
Generate. The built-in `go` generator (`ratel-gen-go`) is registered, so its
request replays too. Output goes to stdout, so shell redirection creates the
replay file:

```sh
ratel dump --format json > catalog.json
ratel dump --plugin go > request.binpb
```

The second command requires the plugin `go` (`builtin: true`) in `ratel.yaml`.

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
      import_path: github.com/acme/app/internal/db

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
- `version: 1` is required. Omitted `postgres.version` defaults to `18`,
  `schema` to `[schema/**/*.sql]`, `migrations` to `migrations`, and `dev.image`
  to `postgres:18-alpine` when neither dev setting is supplied. Explicit
  `dev.image` and `dev.url` are mutually exclusive. Query groups and plugins
  default to empty lists.
  Migration defaults are exactly the values in the example above. Explicit
  nonpositive migration deadlines, batch size or replica-lag bounds are errors;
  backfill pause may be zero. Only `gitlab` is accepted as a retry schedule.
- Each plugin selects exactly one of `builtin: true`, `exec: <path-or-name>` or
  `wasm: <path-or-http(s)-URL>`, and requires a unique `name` and nonempty `out`.
  `sha256` is required for WASM and exec paths, optional for PATH lookups.
  When present it is always checked, including after a cached Info exchange.
  `timeout` defaults to `1m`; zero selects that default and negatives are errors.
  It bounds one host operation, not individual SQL parser calls.
- Plugin `options` is a mapping encoded as UTF-8 JSON bytes (absent options
  encode as `null`). Keys inside it belong to the plugin and are not rejected
  by the host. `Info` describes options; `Generate` validates them. No options
  exist in the Info request. Unknown host keys and multiple YAML documents
  are errors; duplicate keys are errors as well.
- `postgres.version` must be between 16 and the latest major this ratel
  version supports. A planned step records it; `migrate apply` refuses a
  server whose major is lower than the step's or higher than the latest
  supported one.
