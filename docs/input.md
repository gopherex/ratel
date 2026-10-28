# Input

ratel reads PostgreSQL SQL only. Three kinds of sources are configured in
`ratel.yaml` ([cli.md](cli.md)): schema files, query files and the migrations
directory. Every source is parsed by libpg_query (PostgreSQL's own parser), so
anything PostgreSQL rejects is rejected with its message and position.

## Schema: a set of declarative DDL files

The schema is the desired state of the database, written as DDL. It is a **set**
of files: hand-written, emitted by a frontend such as `protoc-gen-ratel` (one
file per `.proto`), or both.

### Allowed statements

| Statement | Notes |
|---|---|
| `CREATE SCHEMA` | |
| `CREATE EXTENSION` | database-wide object |
| `CREATE TYPE` (enum, composite, range), `CREATE DOMAIN` | |
| `CREATE SEQUENCE` | |
| `CREATE TABLE` (incl. partitioned, `PARTITION OF`) | columns, identity, generated (`STORED`/`VIRTUAL` stated explicitly), constraints |
| `CREATE [UNIQUE] INDEX` | `CONCURRENTLY` is not written here; the planner decides |
| `CREATE VIEW`, `CREATE MATERIALIZED VIEW` | |
| `CREATE FUNCTION`, `CREATE PROCEDURE` | |
| `CREATE TRIGGER` | |
| `CREATE POLICY` | |
| `ALTER TABLE ... ADD CONSTRAINT` | attaches to a table declared in the set |
| `ALTER TABLE ... ENABLE / FORCE ROW LEVEL SECURITY` | |
| `COMMENT ON` | stored in the IR as the object's comment |
| `GRANT ... ON <object> TO <role>` | roles are not managed by ratel; they must exist on the server |

Any other statement in a schema file (`DROP`, `INSERT`, `ALTER ... RENAME`,
`ALTER ... TYPE`, transaction control) is an error: the schema states what must
exist, not how to get there. Renames and type changes are declared with
annotations (below).

### Invariants

1. **Order independence.** The Catalog built from a set of files does not depend
   on the order of the files or of the statements inside them. References
   (FKs, types, domains, sequences, functions used in defaults and checks) are
   resolved after all declarations are collected. FK cycles are allowed.
   *Check:* a property test builds the Catalog from random permutations of the
   fixture files and compares the deterministic protobuf serialization
   byte for byte; for random permutations of the statements inside a file it
   compares the serialization after clearing every `SourceSpan`, since spans
   record the original positions.
2. **Uniqueness.** Two declarations of the same object are an error that names
   both positions (`file:line:column`).
3. **Resolution.** A reference to an object that is declared nowhere in the set
   and is not built in is an error at the referencing position.
4. **Determinism.** Building the same set twice yields byte-identical IR.

## Queries

Query files hold named static queries, sqlc-style:

```sql
-- name: UserByEmail :one
SELECT id, email, status FROM app.users WHERE email = @email;

-- name: OrdersOfUsers :many
SELECT * FROM app.orders WHERE user_id = ANY(@user_ids);
```

| Element | Meaning |
|---|---|
| `-- name: <Name> <command>` | starts a query; `Name` is an exported Go-style identifier, unique across all query files |
| `:one` | one row; no row is `ratel.ErrNotFound` |
| `:many` | zero or more rows |
| `:exec` | no rows returned |
| `:execrows` | number of affected rows |
| `:copyfrom` | bulk insert through COPY |
| `@name` | named parameter, rewritten to `$N`; the same name maps to the same `$N` |
| `$N` | positional parameter |

A header occupies its own `--` comment line; text in SQL literals and inline
comments never starts a query. Each header owns exactly one nonempty statement,
ending at the next header or EOF. A terminating semicolon is optional. Query
annotations go after the header and before the first SQL token, for example:

```sql
-- name: UserByEmail :one
-- @ratel.row User
-- @go.row User
SELECT id, email FROM app.users WHERE email = @email;
```

`@ratel.row` is a core annotation; a configured Go plugin owns the meaning of
`@go.row`. The scanner preserves both, without doing type inference or parameter
rewriting. It checks lexical SQL, headers and statement boundaries; inference
parses the statement after named-parameter rewriting.

There is no dynamic-query syntax. Optional filters, dynamic ordering and
pagination are added at runtime by using a `SELECT` query as a builder source
([runtime.md](runtime.md#queries-as-builder-sources)). Parameter and column
types and their nullability are inferred from the Catalog ([ir.md](ir.md));
where inference cannot decide, an annotation states it.

Inference is guaranteed for these constructs, each covered by the test
corpus: `WITH` including `RECURSIVE` (column types from the non-recursive
term) and data-modifying CTEs; subqueries in every position; `LATERAL`;
`UNION`, `INTERSECT`, `EXCEPT`; window functions; `GROUPING SETS`, `ROLLUP`,
`CUBE`; `VALUES`; `unnest ... WITH ORDINALITY`; JSON functions and operators;
`INSERT`, `UPDATE`, `DELETE` and `MERGE` with `RETURNING`; `ON CONFLICT`.
A construct outside this list either infers or fails with a diagnostic; it
never yields an untyped result.

## Migrations directory

Holds the steps planned by `ratel migrate plan` and the integrity file
`ratel.sum`. Format and rules are in [migrations.md](migrations.md).
Hand-written migration files are allowed, and the linter analyses them with
the same rules as planned steps.

## Annotations

Annotations attach metadata that DDL cannot carry: relation names, rename and
retype intent, the proto origin of a column, an explicit permission for an
unsafe migration change. Plugins add their own (Go type overrides are
`@go.type` of `ratel-gen-go`).

### Syntax

```sql
CREATE TABLE app.posts (
	id bigint PRIMARY KEY,
	-- @ratel.rel name=Author inverse=Posts
	author_id bigint NOT NULL REFERENCES app.users (id),
	body text NOT NULL -- @go.type github.com/acme/md.Markdown
);
```

```
annotation := "@" namespace "." name { arg }
arg        := value | key "=" value
value      := bare | quoted
bare       := run of characters other than whitespace, '"' and '='
quoted     := '"' { any character except a line break, '""' for a quote } '"'
```

An annotation starts at the beginning of a comment line, ignoring whitespace
and one optional leading `*` in a block comment. Each line holds one annotation;
mentions inside prose are ordinary text. Namespace, annotation name and argument
keys are identifiers (Unicode letters, digits after the first character, and
underscores). Keys are unquoted, and `=` has no surrounding whitespace. Values
use Unicode whitespace as separators. Duplicate keys are syntax errors in every
namespace.

- One syntax for every namespace: positional values and `key=value` pairs. The
  host parses it and hands plugins the parsed form; the owner of a namespace
  validates meaning (core: the host, plugins: the plugin).
- Annotations live in `--` and `/* */` comments. Attachment:
  - comments directly before a statement attach to the statement's object;
  - inside a column or constraint list, comments directly before an element
    and a trailing comment on its line attach to that element;
  - whitespace and ordinary comments do not break adjacency. A same-line
    trailing comment wins over attachment to the next element, including after
    a comma. Outside the list, a trailing comment has no element target;
  - an annotation before `ALTER TABLE ... ADD CONSTRAINT` attaches to the added
    constraint when exactly one constraint is added; otherwise it targets the
    table. An annotation on an individual constraint still targets that
    constraint. `COMMENT ON` targets its referenced object; `GRANT` targets its
    referenced object only when the statement names one distinct object.
    Multi-object grants have no single annotation target. Annotations without
    an adjacent target are errors.
- The scanner is SQL-aware: text inside string literals, quoted identifiers and
  dollar-quoted bodies is never scanned.
- Core annotations are validated by the host: unknown name, wrong target,
  missing or unknown key, or a repeated single annotation is an error at its
  position.
- An annotation of a namespace that no configured plugin owns is a warning, so
  sources can be shared between projects with different plugin sets.

### Core annotations (namespace `ratel`)

| Annotation | Target | Meaning |
|---|---|---|
| `@ratel.rel name=<Name> [inverse=<Name>]` | FK constraint or FK column | names the relation and its inverse; required when two FKs connect the same pair of tables |
| `@ratel.norel` | FK constraint or FK column | the FK produces no relation |
| `@ratel.renamed from=<old>` | table, column, constraint, index, type | rename intent for the migration engine |
| `@ratel.retype using=<sql expr>` | column | type change intent with the conversion expression |
| `@ratel.backfill expr=<sql expr> [batch=<n>]` | column | how existing rows get a value when the column is added or becomes `NOT NULL` |
| `@ratel.allow <RTL-code> reason=<text>` | schema object; statement in a hand-written migration | explicit permission for a change the planner cannot make safe, or a breaking change in expand; the planner copies it into the step header |
| `@ratel.nullable <param>` / `@ratel.notnull <param or column>` | query | overrides inferred nullability |
| `@ratel.row <Name>` | query | the result type is the shared generated type `<Name>` |
| `@ratel.origin <ref>` | table, column | emitted by frontends: where the object came from (e.g. a proto field) |

Core arguments must be nonempty. Relation names and shared row names are
exported Go-style identifiers, including Unicode uppercase letters. `batch` is
a positive unsigned 64-bit integer; `using` and `expr` must parse as SQL
expressions. Permission codes have the form `RTL-` followed by an uppercase
ASCII letter and two digits.

Core annotations are single-use per target, except `allow` (one per code),
`nullable` and `notnull` (one per referenced name). `rel` conflicts with `norel`;
`nullable` conflicts with `notnull` for the same name. Plugin owners, not the
host, validate their annotation names, values, target kinds and repeatability.

Rename, retype and backfill annotations are consumed by the next planned
migration. `ratel migrate plan` reports annotations whose intent is already
applied, so they can be deleted.
