# IR

The IR is a PostgreSQL-faithful model of a database and its named queries,
defined in protobuf under `proto/ratel/v1/ir` (package `ratel.v1.ir`). The
`.proto` files are the field-level contract; this document states what the
messages mean and which invariants every producer guarantees.

Two producers build the same `Catalog` shape: the catalog builder from schema
files (desired state) and introspection from a live database (actual state).
The migration engine diffs them; output plugins read the built one.

## Content

| Area | Messages | Notes |
|---|---|---|
| Database | `Catalog`, `Schema`, `Extension` | extensions are catalog-level (database-wide), with the schema they are installed in |
| Tables | `Table`, `Column`, `Constraint`, `Index`, `Sequence` | partitioning, identity, generated columns with explicit `STORED` or `VIRTUAL` |
| Types | `TypeRef`, `EnumType`, `DomainType`, `CompositeType`, `RangeType` | canonical PostgreSQL type names (`int4`, `_text`), modifiers kept (`varchar(n)`, `numeric(p,s)`) |
| Code objects | `View`, `MaterializedView`, `Function`, `Procedure`, `Trigger` | overloads keyed by argument signature |
| Security | `Policy`, `Grant`, table RLS flags `rls_enabled`, `rls_forced` | roles are referenced by name, not modeled |
| Validity | `Constraint.validated`, `Index.valid`, `Index.ready` | needed to resume interrupted migrations |
| Not-null | not-null as a named constraint with validity | PostgreSQL 18 stores it in `pg_constraint`; for 16 and 17 introspection synthesizes the same shape |
| Comments | `Metadata.comment` | from `COMMENT ON` and from the database |
| Relations | `Relation` | see below |
| Queries | `Query`, `QueryParameter`, `QueryColumn` | structured statement IR for `SELECT`, `INSERT`, `UPDATE`, `DELETE` and `MERGE` |
| Diagnostics | `Diagnostic` | severity, stable code, message, source span |
| Annotations | `AnnotationValue`, attached to `Metadata` | parsed and validated by the host |

Expression trees keep a `raw_sql` form next to the structured form, so nothing
the parser accepted is lost. `RawStatement` remains only for statements outside
the allowed set of [input.md](input.md) that appear in hand-written migrations.

## Object identity

Every object has an `ObjectRef { kind, name }` where `name` is a
`QualifiedName` of identifier parts (schema, table, column, ...). The string id
used in maps and diagnostics is derived from it by one rule: parts joined by
`.`, each part written as a PostgreSQL identifier (double-quoted when it is not
a lower-case simple identifier). Functions append the argument type list.

- Producers never invent ids by other rules; one Go function in the host
  computes them and its tests cover quoting and overloads.
- References between objects are ids, never nested copies.

## Invariants

1. **Deterministic.** Repeated fields are ordered by object id (columns by
   attribute position, enum values by sort order). The same input produces
   byte-identical deterministic serialization.
2. **Round trip.** The catalog build takes the target major version
   (`postgres.version`), because the stored shape is version-dependent
   (not-null constraints, default generated-column kind). For every allowed
   schema statement, `build(DDL, major)` equals `introspect(apply(DDL))` on a
   server of that major, after normalization by PostgreSQL. *Check:* the
   integration suite applies each fixture to real PostgreSQL 16, 17 and 18 and
   compares.
3. **Positions.** Every object built from source carries a `SourceSpan`;
   introspected objects carry none.
4. **Complete inference or an error.** A query whose parameter or column type
   cannot be inferred fails with a diagnostic naming the expression, unless an
   annotation states the type or nullability. Unknown functions and operators
   never silently become `any`.
5. **Built-in signatures are generated.** The table of built-in functions,
   operators and casts used by inference is generated from `pg_proc`,
   `pg_operator` and `pg_cast` of each supported PostgreSQL major version, not
   maintained by hand.

## Syntax mapper

`cmd/ratel/internal/mapper` adapts the libpg_query AST returned by
`cmd/ratel/internal/parse` to the generated Go messages in `ir/`. It does not
assemble a catalog, resolve references, infer types, derive relations, invent
constraint names, expand serial columns, or apply server-dependent defaults.
The AST is read-only; each call builds a fresh IR tree.

Create a mapper with `mapper.New(parser, file, sql, rawStmt, ordinal)` for each
`ParseResult.stmts` entry. `sql` is the complete original file, not a statement
substring; `ordinal` is the zero-based position in that file. `MapStatement`
handles SELECT, INSERT, UPDATE, DELETE and MERGE. Dedicated `MapCreate*`,
`MapColumn`, `MapConstraint`, `MapIndex`, `MapView` and `MapMaterializedView`
methods map schema declarations. `MapAlterTable` returns an additive table
fragment for ADD CONSTRAINT, ENABLE ROW LEVEL SECURITY and FORCE ROW LEVEL
SECURITY; applying the fragment belongs to the catalog builder. Policy and
index targets remain available on their original AST statements. Embedded
`CreateSchemaStmt.schema_elts` are visited separately by the caller.

Identity and provenance follow these rules:

- `node_id` is the quoted file name followed by `/statement/<ordinal>` and the
  protobuf field path, including zero-based repeated-field indexes. Every
  message with a `node_id` field receives its own path. Object references use
  `QualifiedName.parts`, not this syntax-tree identity.
- Source offsets are zero-based byte offsets, with an exclusive end. Lines and
  columns are one-based; columns count Unicode code points. The root span is
  the `RawStmt` range, excluding its terminating semicolon. Zero statement
  length means the rest of the file, as in libpg_query.
- Objects without their own AST range inherit the enclosing statement span.
  Columns and constraints use their AST start location and the statement end:
  PostgreSQL supplies a start, not an exact element end. Messages without a
  metadata field, such as expressions, use their containing object's source.
- `SourceSQL()` exposes the original statement slice. Top-level SELECT
  `raw_sql` and raw statements retain that slice. Nested SELECTs and fallback
  expressions use libpg_query's deparser. A deparse failure retains the
  enclosing original SQL, never a placeholder string.

The mapper preserves exact decimal literals as text. Arrays carry an
underscore-prefixed PostgreSQL name and their element type. Explicitly
schema-qualified user types retain that qualification; other type categories
are provisional until catalog resolution. Function identities append the
input argument signature, excluding OUT and TABLE arguments. Functions also carry
the declared default-argument count and variadic element type, so user routines
and builtin snapshots expose the same calling metadata. RETURNS TABLE keeps its
column list, and standard SQL RETURN expressions become SELECT bodies.

Syntax and IR do not have identical representational capacity:

- The current parser AST is PostgreSQL 17. Generated columns therefore map to
  STORED; PostgreSQL 18 VIRTUAL syntax requires a parser supporting it.
- Missing owner, implicit sequence, implicit multirange name, resolved target,
  inferred literal type and server-dependent defaults stay absent. New
  declarative indexes are valid and ready; NOT VALID constraints retain their
  unvalidated state.
- The IR's unsigned numeric scale cannot hold a negative scale. Such a type
  keeps its name but omits the modifier. Fractional or out-of-range function
  cost/row estimates cannot fit the IR's integer fields and stay zero. Their
  declarations remain available through the source span and `SourceSQL()`.
- C-language AS strings share one body field and are joined with a newline.
  Standard SQL routine bodies keep the deparsed declaration in `body` as well
  as the SELECT/RETURN portions in `sql_body`; non-SELECT atomic statements
  remain in that declaration because `sql_body` accepts SELECT messages only.
- SELECT has only one structured locking clause; additional clauses remain in
  `raw_sql`. GROUP BY DISTINCT remains in `raw_sql`; it never sets
  `group_by_all`. FROM shapes without an IR variant and expression conflict
  targets remain recoverable from the original SQL rather than being resolved
  or reinterpreted by the mapper.
- `MapGrant` expands explicit object grants. It returns nil for REVOKE,
  schema-wide grants and column-level privileges, which the declarative IR
  cannot represent. It never widens column privileges to table privileges.
  `MapComment` returns an unresolved object reference and metadata; attachment,
  annotations and declaration validation belong to their host stages. Type and
  domain targets retain their identifier parts in both comments and grants.
  Their object kind stays unspecified until the catalog resolves whether the
  target is an enum, domain, composite or range.

Run mapper/parser tests with
`cd cmd/ratel && go test -race ./internal/...`. Construct one shared
`parse.Parser` with `parse.New(config)` before starting concurrent work; the
constructor warms WASM, and parsing, scanning and deparsing share its configured
semaphore. Tests construct one shared parser in each package's parent test.
Benchmarks use
`cd cmd/ratel && go test -run '^$' -bench . -benchmem ./internal/mapper`.

## Catalog builder

`cmd/ratel/internal/catalog.Build(parser, files, major, builtins)` accepts a
shared parser, complete `File{Name, SQL}` sources, a PostgreSQL major from 16
through 18, and schemas
containing the target server's built-in function signatures. Built-in signatures
are an explicit input, not a hand-maintained function whitelist. They are not
copied into the desired catalog. A type name is built in when the snapshot's
`builtin_types` or pgx's type registry knows it, so `regclass` and other types
pgx lacks resolve to `pg_catalog`, as PostgreSQL's implicit search path does.
With an empty signature input, calls to ordinary built-in functions are
unresolved; sequence calls (`nextval`, `currval`, `setval`) resolve their
constant sequence names directly.

### Generated builtins

`cmd/ratel/internal/builtins.For(major)` returns caller-owned `[]*ir.Schema`
for PostgreSQL 16, 17 or 18; unsupported versions return `*UnsupportedMajorError`.
Pass this result to `catalog.Build`. The snapshots are committed protobuf
`Schema` messages in `cmd/ratel/internal/builtins/data/{16,17,18}.binpb`, embedded
in the host. Loading them requires neither Docker nor a database connection.

Regenerate with `make builtins`, or
`cd cmd/ratel && go generate ./internal/builtins`. The generator starts a pristine
testcontainers PostgreSQL server for each major, reads its system catalogs,
and removes its containers and connections. Image digests in
`internal/builtins/gen/main.go` pin the extraction inputs. The generator's
`-timeout` flag sets its total deadline and cleanup timeout (default 10 minutes).
To intentionally update server images, update those digests and regenerate.

Only `pg_catalog` objects are included; procedures and other namespaces are
excluded. No extra extensions are installed beyond the image's default plpgsql. Database-local OIDs, owners and source metadata are
not serialized. The extraction retains:

- `Schema.builtin_types`: canonical `TypeRef` names and kinds, array elements,
  `pg_type` category/preferred flags, domain bases and range/multirange subtypes.
- `Schema.functions`: all function, aggregate and window signatures, ordered
  arguments with names and IN/OUT/INOUT/VARIADIC modes, scalar or SETOF returns,
  strictness and volatility. TABLE outputs use OUT mode; their column names and
  types remain arguments. `routine_kind` retains `pg_proc.prokind`,
  `default_arguments` retains `pronargdefaults`, and `variadic_element` retains
  `provariadic`. `aggregate` retains `pg_aggregate.aggkind` and the direct
  argument count. These are calling signatures, not function bodies or complete
  migration definitions.
- `Schema.builtin_operators`: operator name, left/right operand types, result
  type and implementing function identity; a prefix operator has no left type.
- `Schema.builtin_casts`: implicit and assignment casts, their source/target
  types, PostgreSQL context/method codes and optional implementation identity.
  Explicit-only casts are excluded. PostgreSQL's generic array/domain/I/O
  coercion rules are not extra rows in `pg_cast` and are not synthesized here.

Function identities use the existing three-part schema/name/input-signature
convention. Sorting canonicalizes catalog sets without reordering argument
positions. Deterministic protobuf serialization and pinned inputs make repeated
extractions byte-identical. Unit tests exercise reversed input order and known
PostgreSQL signatures, version-specific functions and independent loader state.
Run `cd cmd/ratel && go test -race ./internal/builtins/...`; benchmarks use
`go test -run '^$' -bench . -benchmem ./internal/builtins/...` in that module.
The catalog resolver chooses an overload for calls in schema expressions
(defaults, checks, generated columns, index expressions, policies) after
PostgreSQL's `func_select_candidate`. Arity counts trailing defaults and expands
a variadic element. Each argument is typed from its column (the column type,
domains unwrapped to their base), a cast target, or a literal (`int4` or `int8`
by range, `numeric`, `bool`); string literals and NULL are unknown and match any
parameter. An exact type scores above a domain over it, which scores above an
implicit cast from `builtin_casts`; polymorphic parameters accept their type
family. The single best candidate wins; a tie is `RTL-C03`. Inside view and
routine query bodies, column types belong to the query's own scope, so a tie
there leaves the call without a target and query inference types it.

The builder uses `public` as the default schema and search path. It collects
schema declarations, visits embedded `CREATE SCHEMA` elements with that schema
as their namespace, collects objects, and canonicalizes routine signatures after
their argument types are available. Only then does it attach additive table
fragments, indexes, policies, triggers, comments and grants and resolve
references. FK cycles are valid. Missing objects and columns fail the build;
roles remain external names. Non-declarative statements and grants that the IR
cannot represent fail rather than being ignored.

Serial columns expand into integer columns, defaults and owned sequences;
identity columns receive owned sequence references. Sequence defaults preserve
explicit zero-valued options and account for descending increments and integer
width. Non-null columns receive named not-null constraints. Generated names are
PostgreSQL's: `<table>_pkey`, `<table>_<columns>_key`, `<table>_<columns>_fkey`,
`<table>_<column>_not_null`, `<table>_<columns>_excl`, `<table>_<column>_check`
when the check references exactly one column and `<table>_check` otherwise,
`<domain>_check` for domain checks, `<table>_<columns>_idx` for indexes (an
expression element is named after its function, else `expr`) and
`<table>_<column>_seq` for owned sequences. As in `makeObjectName`, the table
and column parts lose trailing bytes, the longer first, until the name fits 63
bytes; the label never shrinks. A collision adds a counter to the label
(`a_x_check1`). Explicit names are reserved first; unnamed constraints are
numbered in declaration order (the `CREATE TABLE` elements, then attached
constraints in sorted order), relation names are unique per schema. Indexes
attach to tables and materialized views.

`catalog.ObjectID` is the host's object identity formatter. Routine identities
append their input argument signature. Declaration uniqueness follows
PostgreSQL relation, type and routine namespaces; table-local objects use their
parent table's identity. Duplicate errors include both source positions. Typed
`catalog.Error` refusals expose stable codes: `RTL-C01` for the target version,
`RTL-C02` for duplicates, `RTL-C03` for unresolved references, and `RTL-C04` for
statements outside the declarative contract.

Catalog expression `node_id` values use canonical catalog field paths after
sorting, not source statement ordinals. Source spans retain the original
positions. File-permutation tests compare deterministic protobuf bytes;
statement-permutation tests clear only source spans before comparing.

`tests/testdata/catalog/schema.sql` is shared by builder permutation tests and
`tests/catalog_test.go`. The integration test applies it to PostgreSQL 16, 17
and 18 and checks catalog facts through `pg_catalog`. The build/introspect
round trip is in [Introspection](#introspection). Run it with
`make test-integration`.

Build parses files serially. Independent concurrent callers inject the same
parser; its semaphore also bounds embedded-schema scanning, mapper deparsing,
routine/sequence reference parsing and annotation validation.
Run builder tests and benchmarks with
`cd cmd/ratel && go test -race ./internal/catalog` and
`cd cmd/ratel && go test -run '^$' -bench . -benchmem ./internal/catalog`.

## Introspection

`cmd/ratel/internal/introspect.Load(ctx, parser, source, namespaces)` reads a
live PostgreSQL 16, 17 or 18 database into a Catalog. It has no mapping of its
own: `Render` makes PostgreSQL write the namespaces as declarative DDL with its
own deparse functions (`format_type`, `pg_get_expr`, `pg_get_constraintdef`,
`pg_get_indexdef`, `pg_get_viewdef`, `pg_get_functiondef`,
`pg_get_triggerdef`), and that text goes through `catalog.Build` with the
server's major and builtin snapshot. Source and database share one mapping path,
so both produce the same shape.

- One `REPEATABLE READ READ ONLY` transaction with `search_path = ''`, so every
  name is schema-qualified. `source` is a `*pgx.Conn` or `*pgxpool.Pool`; a
  transaction is not accepted, since its isolation is already fixed. The caller's
  context carries the deadline. Any failure returns no catalog.
- Objects created by an extension, and objects with an internal dependency
  (range constructors, identity sequences, partition clones of constraints,
  indexes and triggers), are left to their owner. Extensions are database-wide.
- Clauses equal to PostgreSQL defaults are not rendered. A sequence that is
  exactly what a `serial` column creates renders as that column; any other
  sequence renders itself.
- Privileges of the owner and the defaults of `acldefault` (`EXECUTE` on
  routines, `USAGE` on types to `PUBLIC`) are not grants.
- Facts only DDL carries are absent: source spans, annotations and what they
  derive (named relations, annotation diagnostics). Owners are environment
  facts and are not read. `Catalog.name` is the database name.
- The rendered text is ordered by creation dependency (types by type
  dependencies, routines before tables, views by view dependencies, foreign keys
  after keys and indexes), so with `check_function_bodies = off` it applies to an
  empty database.

`cmd/ratel/tests/introspect_test.go` checks, on PostgreSQL 16, 17 and 18, that
`build(DDL)` equals `Load(apply(DDL))` for `tests/testdata/catalog/schema.sql`
and `cmd/ratel/tests/testdata/introspect/schema.sql`, after PostgreSQL's
normalization: expressions are compared without casts (PostgreSQL stores every
implicit coercion as an explicit cast), query bodies by structure rather than
deparsed text, an extension declared without `VERSION` matches any installed
version, and a grant of a default privilege is no grant. It also applies the
rendered DDL to an empty database and requires the same rendering back, and
that a lost connection yields an error and no catalog. The migration diff
compares two catalogs that both went through PostgreSQL ([migrations.md](migrations.md)),
so it needs none of these normalizations.

## Annotation scanner and query sources

`cmd/ratel/internal/annotate.Scan(parser, file, sql)` reads libpg_query comment tokens
and returns parsed annotations with both comment ranges and exact annotation
spans. Byte offsets have exclusive ends; columns count Unicode code points.
Strings, quoted identifiers and dollar-quoted bodies never produce annotations.
Inject the application parser so scanning shares its concurrency bound.

`annotate.Attach(parser, file, targets, ownedNamespaces)` selects the adjacent declared
object, validates core annotations and appends them to `Metadata.annotations`
in source order. Each annotation carries its resolved `ObjectRef` and its own
source span. A target can supply a separate attachment span for an additive
statement without replacing the declaration's provenance. Composite fields and
domain constraints carry `Metadata` too (`CompositeField.meta`, field 4;
`DomainConstraint.meta`, field 3).

`catalog.Build` runs attachment after collecting and resolving declarations,
including table-local objects and additive constraints. Embedded `CREATE SCHEMA`
declarations get separate mapper ranges, so their leading annotations don't
attach to the enclosing schema. Core failures return `*annotate.Error`, whose
`Diagnostic` has code `RTL-A01` and a `file:line:column` position. Plugin syntax
survives unchanged. Unknown namespace owners produce warning `RTL-A02`;
`Catalog.diagnostics` (field 9) retains these warnings in file/source order.
`Build` has no plugin ownership input and therefore reports all non-core
namespaces; the annotation APIs accept owned namespace names explicitly.

`annotate.Queries(parser, files, ownedNamespaces)` returns a `QuerySet` containing typed
`QuerySource` records and namespace warnings. Files are ordered by filename and
queries retain source order inside each file. Names are unique across the whole
input. Each record carries its name, `QueryCommand`, exact original SQL token
range, SQL metadata span, header span and query-targeted annotations. This is
not an inferred `ir.Query`: named parameters remain unchanged, and the later
inference stage supplies rewritten SQL, AST and signatures. Header, annotation
and statement-boundary errors use the same `RTL-A01` diagnostic contract.

Run scanner tests and benchmarks with
`cd cmd/ratel && go test -race ./internal/annotate` and
`cd cmd/ratel && go test -run '^$' -bench . -benchmem ./internal/annotate`.

## Named-query inference

`cmd/ratel/internal/query.Infer(parser, catalog, builtins, sources)` accepts the
shared `parse.Parser`, a built catalog, the matching `builtins.For(major)`
signatures and `annotate.QuerySource` records. It returns caller-owned
`[]*ir.Query`, in source order, without changing the catalog or source metadata.
The output includes rewritten positional SQL, mapped statement IR, ordered
parameter signatures and output columns. Physical relation references in the
output SQL carry their resolved schema. AST metadata retains the original query
source range; statement ordinals distinguish queries in the same file.

Inference uses lexical scopes for CTEs, derived tables, LATERAL and correlated
subqueries. Object-name lookups use `catalog.ObjectID`, so quoted identifiers
cannot collide with identifier-part separators. Recursive CTEs take their types from the non-recursive term and
propagate nullability until it stops changing, so NULL introduced in one
iteration can reach another output column in a later iteration. Set operations
choose a common type using the snapshot's implicit casts. Parameter contexts
include write targets, operators, function arguments, scalar-array comparisons,
pagination and window-frame offsets. Each parameter occurrence retains its own
coercion state, while all occurrences share one positional type; a constraint on
one occurrence cannot hide an unresolved occurrence elsewhere. Targets constrain
parameters before WHERE, and window frames and INSERT assignment contexts run
later. RANGE frame offsets use the snapshot's `in_range` signatures, including
inherited windows, rather than assuming an integer offset.

Overload candidates come from both catalog routines and generated builtins.
Exact argument types, implicit coercions, preferred types and unknown string
arguments participate in selection; array and element polymorphism, default
arguments and variadic calls retain their declared signatures. Domain coercions
use the declared base type without discarding an exact domain overload. Common
types retain a domain only when every input has that domain type. Table functions
expand declared OUT/TABLE fields or the known composite/table-row layout;
otherwise a record needs an SQL column-definition list. Unresolved or
ambiguous calls fail instead of selecting the first candidate. Query inference
resolves `abs(1)` and `abs(integer_column)` without adding an explicit cast;
the separate schema-expression resolver still has the limitation stated above.

Direct projections retain `QueryColumn.source_column` and `table_alias`, even
when an output label changes. Computed values do not acquire provenance from a
matching label. Outer joins change nullability, not the source identity; qualified
stars retain the individual sides of a USING join. Parameters inferred from write
columns retain their source column and nullability. A parameter used in several
column contexts defaults to nullable only when every such context is nullable;
an explicit query annotation overrides that default.

Core row annotations and plugin annotations pass through `Query.meta` unchanged.
The output plugin chooses or validates the Go row type from this metadata and
the inferred column provenance; inference does not import or inspect Go types.

Incomplete types, ambiguous references, unknown functions/operators and invalid
parameter numbering return `*query.Error`, with an ERROR `Diagnostic` of code
`RTL-Q01` naming the query and failed expression. Syntax errors retain the parser's
error. There is no successful partial signature or fallback `any` type.

Unit tests share one parser per parent test and run inference cases concurrently:
`cd cmd/ratel && go test -race ./internal/query`. Benchmarks use
`go test -run '^$' -bench . -benchmem ./internal/query` in that module.
`cmd/ratel/tests/query_test.go` prepares each fixture with pgx on PostgreSQL 16,
17 and 18 and compares described parameter/result OIDs and output names with the
inferred signature. RowDescription exposes a domain's base OID, so this comparison
normalizes domain outputs through `pg_type`; separate `pg_typeof` assertions
check that inference preserves the nominal domain when PostgreSQL does. Fixtures
without parameters also execute against real rows, including RETURNING; an actual
NULL must never occupy an inferred non-null column. Refusal cases compare the
inference diagnostic with PostgreSQL's unknown/ambiguous-function and inconsistent
parameter SQLSTATEs.
`make test-integration` includes this suite. Inference itself has no connection or
server dependency.

## Relations

A `Relation` is a navigable edge used by the ORM:

| Field | Meaning |
|---|---|
| `name`, `inverse_name` | Go-facing names; from `@ratel.rel` or derived from the FK |
| `kind`, `inverse_kind` | `ONE` (to-one) or `MANY` (to-many) cardinality of the navigation from `from` and from `to` |
| `from`, `to` | table ids with column lists (the FK columns and the referenced columns) |
| `through` | join table id for many-to-many |
| `fk` | id of the FK constraint the relation comes from (for many-to-many, the first join key) |

Rules:

- Every FK (not `@ratel.norel`) yields one relation: `kind` ONE on the
  referencing table, `inverse_kind` MANY on the referenced table, or ONE when
  the FK columns are exactly the primary key or a unique constraint.
- Derived names are Go-facing (`PascalCase`):
  - to-one name: each FK column without its `_id` suffix, PascalCase, joined in
    column order (`author_id` -> `Author`, `tenant_id, user_id` -> `TenantUser`);
  - inverse name: the referencing table name, PascalCase (`posts` -> `Posts`).
    No pluralization is done.
- `@ratel.rel name=N inverse=M` replaces the to-one name `N` and inverse `M`.
  A column-level or constraint-level annotation applies to the whole FK; having
  more than one relation annotation on one FK is an error.
- A table whose primary key is covered by exactly two FKs, which are disjoint
  and together make up the key, is a join table. It yields a many-to-many
  relation `MANY`/`MANY` from the first referenced table to the second, with
  `through` set to the join table. Its FK-derived relations stay. The name
  toward the second table is the `name` of the join key to the second table,
  or the second table name in PascalCase; the inverse is named by the first
  join key the same way. A many-to-many relation is suppressed when either
  join key is `@ratel.norel`.
- Two FKs between the same pair of tables (ignoring `@ratel.norel` FKs) need
  `@ratel.rel` on each; a missing one is an error at its FK position.
- A derived name that is not an exported Go identifier is an error at the FK
  position; declare `@ratel.rel name=` instead.
- A relation name that is already taken on its table by a column (compared as
  PascalCase) or by another relation is an error naming both.
- Diagnostic codes: `RTL-C05` ambiguous pair, `RTL-C06` conflicting relation
  annotations, `RTL-C07` underivable name, `RTL-C08` name collision.
- `Catalog.relations` is ordered by the FK object id, then by `through`.
- Polymorphic relations (type plus id columns) are not modeled.

## Versioning

The package is `ratel.v1`. Within v1 changes are additive; `easyp breaking`
guards it. The plugin protocol carries its own version number
([plugins.md](plugins.md)).
