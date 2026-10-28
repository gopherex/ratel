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
