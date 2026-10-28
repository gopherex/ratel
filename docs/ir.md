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

Create a mapper with `mapper.New(file, sql, rawStmt, ordinal)` for each
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
input argument signature, excluding OUT and TABLE arguments. RETURNS TABLE
keeps its column list, and standard SQL RETURN expressions become SELECT
bodies.

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
`cd cmd/ratel && go test -race ./internal/...`. Parallel parser users call
`parse.Warm()` before starting concurrent parsing. Benchmarks use
`cd cmd/ratel && go test -run '^$' -bench . -benchmem ./internal/mapper`.

## Catalog builder

`cmd/ratel/internal/catalog.Build(files, major, builtins)` accepts complete
`File{Name, SQL}` sources, a PostgreSQL major from 16 through 18, and schemas
containing the target server's built-in function signatures. Built-in signatures
are an explicit input, not a hand-maintained function whitelist. They are not
copied into the desired catalog. Scalar built-in type names use pgx's type
registry. With an empty signature input, calls to ordinary built-in functions
are unresolved; sequence calls (`nextval`, `currval`, `setval`) resolve their
constant sequence names directly.

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
width. Non-null columns receive named not-null constraints. Unnamed constraint
names are allocated deterministically, reserving explicit names first. Unnamed
indexes use column-based names and reserve explicit relation names before
allocating collision suffixes.

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
and 18 and checks catalog facts through `pg_catalog`. The introspection producer
is not implemented, so this test does not claim the full build/introspect
round-trip equality. Run it with `make test-integration`.

Build parses files serially. Independent concurrent callers warm the parser
with `parse.Warm()` before starting, as required by the parser's wazero cache.
Run builder tests and benchmarks with
`cd cmd/ratel && go test -race ./internal/catalog` and
`cd cmd/ratel && go test -run '^$' -bench . -benchmem ./internal/catalog`.

## Relations

A `Relation` is a navigable edge used by the ORM:

| Field | Meaning |
|---|---|
| `name`, `inverse_name` | Go-facing names; from `@ratel.rel` or derived from the FK |
| `kind` | `ONE` (to-one) or `MANY` (to-many) |
| `from`, `to` | table ids with column lists |
| `through` | join table id for many-to-many |
| `fk` | id of the FK constraint the relation comes from |

Rules:

- Every FK yields a to-one relation on the referencing table and a to-many
  inverse on the referenced table (to-one when the FK columns are unique),
  unless `@ratel.norel` is set.
- A table whose primary key consists of exactly two FK column sets is a join
  table and yields a many-to-many relation between the two referenced tables.
  The join table stays an ordinary model with its own columns.
- Two FKs between the same pair of tables without `@ratel.rel` names are an
  error.
- Derived names that collide with each other or with a column name are an
  error naming both.
- Polymorphic relations (type plus id columns) are not modeled.

## Versioning

The package is `ratel.v1`. Within v1 changes are additive; `easyp breaking`
guards it. The plugin protocol carries its own version number
([plugins.md](plugins.md)).
