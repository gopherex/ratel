# Runtime and generated Go code

The runtime is package `github.com/gopherex/ratel` (`ratel`). `ratel-gen-go`
generates thin packages on top of it: models, table descriptors with typed
columns and relations, reflection-free scanners, insert types, enum types,
type registration and static queries. Everything else is generic runtime code
built on Go 1.27 generic methods.

Models are generated from the schema only (SQL files, or proto through
`protoc-gen-ratel`). There is no Go-first schema: the schema is what
migrations are planned from. Go types a user writes by hand stay the user's;
ratel can scan query results into them (`@go.row`, below) but never derives
schema from them.

## Executor and DB

```go
// Executor is the only way ratel reaches PostgreSQL.
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error)
}

db := ratel.New(exec, ratel.WithTracerProvider(tp), ratel.WithExecMode(pgx.QueryExecModeCacheDescribe))
```

- `*pgxpool.Pool`, `*pgx.Conn`, `pgx.Tx` and `github.com/gopherex/pgtx`'s
  `tx.DB` satisfy `Executor` without adapters. *Check:* a compile-time
  assertion test per type.
- ratel creates no pools, opens no connections and has no transaction API.
  Transactions, isolation levels, savepoints for nested units and retry of
  serialization failures come from the executor; with pgtx (v1.1.4 or newer),
  every ratel call inside `Do*` runs in the transaction carried by `ctx`, and
  concurrent use of that transaction from several goroutines is refused with
  pgtx's `tx.ErrTxInUse`, which ratel returns wrapped and unchanged.
- `*ratel.DB` is a concrete type: generic methods cannot be declared on
  interfaces. It is safe for concurrent use when its executor is.
- `WithExecMode` (and the per-call option `ratel.ExecMode(mode)`) passes a pgx
  query exec mode as the first query argument; it is needed behind PgBouncer
  in transaction mode. Default: pgx's default.
- `WithTracerProvider` defaults to the global OpenTelemetry provider. ratel
  never logs and records no metrics; pool metrics belong to the pool owner.
- There are no hooks or interceptors. Data invariants such as `updated_at`
  belong in the schema (defaults, `BEFORE` triggers): values set by a `BEFORE`
  trigger are returned by `RETURNING`, and they hold for every writer.

## Generated code

Packages follow PostgreSQL schemas: tables, views and types of schema `app`
go to package `app`.

```go
type User struct {
	ID        int64
	Email     string
	Status    UserStatus      // enum type generated from the Catalog
	ManagerID ratel.Null[int64] // nullable column
	CreatedAt time.Time
	R         UserRelations     // loaded relations, tri-state
}

func (u *User) Orders() ([]Order, error) // ratel.ErrNotLoaded unless loaded

type UserInsert struct {          // columns without a default are required
	Email     string
	Status    UserStatus
	ManagerID ratel.Null[int64]    // nullable, no default: zero value inserts NULL
	ID        ratel.Opt[int64]     // identity: omitted unless set
	CreatedAt ratel.Opt[time.Time] // DEFAULT now(): omitted unless set
	// a nullable column with a default would be ratel.Opt[ratel.Null[T]]
}

var Users = usersTable{...}       // descriptor: immutable, dependency-free, generated code only

type usersTable struct {
	ratel.Table[User]
	ID        ratel.Col[User, int64]
	Email     ratel.Col[User, string]
	Status    ratel.Col[User, UserStatus]
	ManagerID ratel.NullCol[User, int64]
	CreatedAt ratel.Col[User, time.Time]
	Orders    ratel.Rel[User, Order]
	Manager   ratel.Rel[User, User]
}

func RegisterTypes(ctx context.Context, conn *pgx.Conn) error // for pgxpool AfterConnect
```

Rules:

- The model is a plain struct; columns, scanner and relations live in the
  descriptor. Nullable columns are `ratel.Null[V]` fields in the model and
  `NullCol[M, V]` in the descriptor, so operators take `V`
  (`ManagerID.Eq(int64)`, `IsNull()`).
- Optional values are value types, never pointers:
  - `ratel.Null[T]`: a value or SQL NULL; `ratel.NullFrom(v)`,
    `ratel.NullFromPtr(p)`, `Get() (T, bool)`, `GetOr(fallback)`, `Ptr()`;
  - `ratel.Opt[T]`: set or not set (not set = the column is omitted and its
    default applies); `ratel.OptFrom(v)`, `Get() (T, bool)`;
  - both implement `IsZero()` (NULL, respectively not set), so JSON's
    `omitzero` skips them, and scan and encode through pgx codecs natively,
    without `database/sql` interfaces.
- Non-null columns are plain fields: a wrapper would add a state the column
  cannot have.
- One model type per table, shared by the builder and by static queries.
  There are no bridge conversions between model kinds.
- Scanners and argument lists are generated code. *Check:* the linter forbids
  importing `reflect` in package `ratel` and in code generated by
  `ratel-gen-go`.
- The insert type makes a missing required column a compile error; `ratel.Opt`
  marks columns with a default, identity or generated value.
- Enums come from the Catalog: a PostgreSQL enum type, or a `text` column with
  an `IN (...)` check, becomes a Go string type with one constant per value.
- Views and materialized views get a model and a read-only descriptor (no
  write methods).
- Composite primary and foreign keys are supported everywhere; batched loading
  binds them as parallel arrays through `unnest($1::t1[], $2::t2[])`.
- A table without a primary key has `Update().Where()` and `Delete().Where()`
  but no methods that address one row by identity (`Save`, `Delete(model)`,
  `Rel` targets); they are absent from its descriptor type.
- Generated identifiers come from one naming function (singular model, plural
  descriptor, Go initialisms). A collision is a generation error naming both
  sources, never a silent suffix.
- `RegisterTypes` registers every enum, domain, composite and range type the
  package uses.

### Type mapping

| PostgreSQL | Go |
|---|---|
| `int2`, `int4`, `int8` | `int16`, `int32`, `int64` |
| `float4`, `float8` | `float32`, `float64` |
| `numeric` | `decimal.Decimal` (`github.com/shopspring/decimal` through `github.com/jackc/pgx-shopspring-decimal`) |
| `text`, `varchar`, `char` | `string` |
| `bool` | `bool` |
| `bytea` | `[]byte` |
| `uuid` | `uuid.UUID` (`github.com/google/uuid`) |
| `timestamptz`, `timestamp`, `date` | `time.Time` |
| `time` | `pgtype.Time` |
| `interval` | `pgtype.Interval` (months and days are not durations) |
| `inet`, `cidr` | `netip.Addr`, `netip.Prefix` |
| `json`, `jsonb` | `json.RawMessage`, or the type from `@go.type` |
| ranges, multiranges | `pgtype.Range[T]`, `pgtype.Multirange[...]` |
| arrays | slices of the element type |
| enum, composite, domain | generated types |

Every mapping can be overridden in the plugin options per type or per column
(`@go.type`). An override type must have a pgx codec; `RegisterTypes` wires
the codecs the package needs.

## Static queries

Query files are grouped into Go packages in `ratel.yaml`
([cli.md](cli.md)); every named query becomes a function there:

```go
func UserByEmail(ctx context.Context, db *ratel.DB, email string) (User, error)
func ActiveUsers(ctx context.Context, db *ratel.DB, since time.Time) ([]User, error)
func ActiveUsersAll(ctx context.Context, db *ratel.DB, since time.Time) iter.Seq2[User, error]
```

Result types are chosen by the provenance of the result columns (inferred
source columns, not names), so `Row` types appear only when nothing existing
fits:

| Select list | Go result |
|---|---|
| every column of one table, any order | the table model; the scanner follows the query's column order |
| every column of one table plus computed columns | `<Query>Row { app.User; OrderCount int64 }` |
| every column of several tables | `<Query>Row { User app.User; Manager app.User }`; a table on the nullable side of an outer join is a pointer, `nil` when its primary key is NULL |
| anything else | `<Query>Row` with one field per column |
| any shape, query annotated `@ratel.row <Name>` | shared type `<Name>`; every query with that name must have the same shape, else a generation error |
| any shape, query annotated `@go.row <import path>.<Type>` | the user's own struct; fields are matched to columns by name and type through `go/types`, and a mismatch is a generation error |

Two queries with identical shapes and no `@ratel.row` produce a warning that
suggests one.

- `:one` returns `ratel.ErrNotFound` for no row.
- More than three parameters become a generated `<Query>Params` struct.
- Arrays of scalars and of composite types map natively. `json` and `jsonb`
  values get a Go type only through `@go.type`; ratel does not guess the shape
  of JSON built in SQL.

### Queries as builder sources

Every `:one` and `:many` `SELECT` query also gets a read-only descriptor, so
runtime filters, ordering and pagination can be added to SQL that the builder
does not express (CTEs, window functions, aggregates):

```go
db.From(app.ActiveUsersQ.Args(since)).
	Where(app.ActiveUsersQ.Email.Like("%@acme.com")).
	OrderBy(app.ActiveUsersQ.CreatedAt.Desc()).Limit(20).List(ctx)
```

- It renders `SELECT ... FROM (<query>) q WHERE ... ORDER BY ... LIMIT ...`;
  the query's parameters keep `$1..$n` and the builder's continue from `$n+1`.
- Added clauses apply to the query's **result**: when the query itself has
  `LIMIT`, aggregates, window functions or `DISTINCT ON`, a filter added
  outside narrows that result, it does not run before them. This is the defined
  meaning and is stated in the generated doc comment of such descriptors.
- The builder itself has no CTE API; complex shapes are written in SQL and
  composed this way.

## Query builder

```go
u, err := db.From(app.Users).Where(app.Users.Email.Eq(email)).One(ctx)
us, err := db.From(app.Users).Where(app.Users.Status.In(app.UserActive)).
	OrderBy(app.Users.ID.Asc()).Limit(100).List(ctx)
for u, err := range db.From(app.Users).All(ctx) { ... }
ids, err := db.From(app.Users).Pluck(app.Users.ID).List(ctx)        // []int64
n, err := db.From(app.Users).Where(...).Count(ctx)
```

- `Where` takes `ratel.Cond[M]` for the query's model only; a condition on
  another table does not compile. Joins take `ratel.Cond2[M, M2]`
  (`app.Users.ManagerID.EqCol(app.Users.ID)` style column comparisons).
- `Col[M, V]` has comparison, `In`, range and pattern operators typed by `V`;
  `NullCol[M, V]` adds `IsNull` and `IsNotNull`.
- Terminals: `One` (`ErrNotFound`), `List` (slice), `All` (`iter.Seq2`),
  `Count`, `Exists`, and `Pluck(col)` with `List` and `All`.
- Projections into other shapes go through queries with `@ratel.row` or
  `@go.row` used as builder sources, or through `Pluck`.
- Pagination: `Limit`, `Offset`, and keyset `After(cols, values)`.
- Locking: `ForUpdate`, `ForShare`, with `SkipLocked` and `NoWait`.
- Escape hatch: `ratel.Raw[M](sql, args...)` yields a `Cond[M]` fragment with
  `$N` renumbered by the builder.

## Writes

- `Insert(UserInsert...)` with `Returning` scanning into the model.
- `Upsert(UserInsert...).OnConflict(cols...).Update(cols...)`; many rows are
  sent as one statement over `unnest` of parallel arrays, chunked by
  `ratel.WithBatchRows(n)`, on any executor.
- `Update().Set(col.To(v)...).Where(...)` and `Delete().Where(...)` on any
  table, with `Returning`. Without `Where` they need an explicit `.All()`
  marker, or they do not compile into SQL.
- `Save(model)` and `Delete(model)` address the row by primary key.
- Optimistic locking: `.ExpectVersion(col, v)` on `Update` and `Save` adds the
  version predicate and returns `ratel.ErrStaleVersion` when no row matched.
- `CopyFrom(app.Users, seq iter.Seq[UserInsert])` streams rows through COPY
  without buffering them all.
- `db.Batch(ctx, items...)` sends several prepared operations (static queries
  and builder statements, each producing a `ratel.BatchItem`) in one round
  trip and scans each result into its own destination.

## Relations

```go
users, err := db.From(app.Users).
	With(app.Users.Orders.Where(app.Orders.Status.Eq(app.OrderPaid)).
		OrderBy(app.Orders.CreatedAt.Desc()).Limit(5).
		With(app.Orders.Items)).
	WithCount(app.Users.Orders).
	List(ctx)

err = db.Load(ctx, users, app.Users.Manager)          // any []User, e.g. from a static query

active, err := db.From(app.Users).Where(app.Users.Orders.Has(app.Orders.Status.Eq(app.OrderPaid))).List(ctx)
```

- Loading is eager and explicit. Models hold no database handle; there is no
  lazy loading.
- `db.Load` loads relations onto any slice of models, so results of static
  queries get relations the same way builder results do.
- Default strategy: one query per relation per depth, `WHERE fk = ANY($1)`;
  relations of the same depth go in one `pgx.Batch`. Nested relations load
  before results are attached to parents.
- `Limit` and `Offset` inside a relation are per parent, through
  `unnest($1) CROSS JOIN LATERAL (... LIMIT n)`.
- Counts per parent through one batched `GROUP BY fk` query.
- The strategy is selectable per relation for data-specific cases:
  `ratel.Window()` for per-parent limits with `row_number()`,
  `ratel.Correlated()` for counts, `ratel.Joined()` for to-one relations.
- Parent filters: `Has`, `HasNone`, `HasCount(op, n)` and `Every`. `Every` is
  true for a parent with no children, as documented on the method.
- A query with `With` returns a type that has no `All` iterator: relations need
  the parent keys first, and loading per row would hold the cursor open. For
  streaming with relations it has `Chunks(ctx, n) iter.Seq2[[]M, error]`, which
  reads parents in keyset pages of `n` and loads relations per page.
- Writes per relation: `Create`, `Attach`, `Detach`, `Sync` on the relation
  descriptor, one statement each. `Sync` (make the set of linked children equal
  a given set) is one statement with data-modifying CTEs (`DELETE` of extra
  links, `INSERT ... ON CONFLICT DO NOTHING` of missing ones), so it is atomic
  on any executor. There are no nested graph writes.
- Loaded relations sit in the model's `R` field; accessors return
  `ratel.ErrNotLoaded` for a relation that was not requested.

## Errors

| Error | Class | Meaning |
|---|---|---|
| `ratel.ErrNotFound` | expected refusal | `One` found no row |
| `ratel.ErrStaleVersion` | expected refusal | optimistic-locking predicate matched no row |
| generated `Err<Constraint>` values, `*ratel.ConstraintError` | expected refusal | unique, FK, check, not-null or exclusion violation; carries kind, constraint, table, columns; matched with `errors.Is` and `errors.As` |
| `ratel.ErrNotLoaded` | programming error | accessor of a relation that was not loaded |
| wrapped executor errors | technical failure | driver, network, timeout; the cause stays reachable through `errors.As` |

ratel does not capture stacks; it never turns an unrecognized failure into a
refusal.

## Schemas and tenants

- Generated SQL always qualifies tables with their schema.
- `app.Users.InSchema("tenant_42")` returns the same descriptor for another
  schema with the same structure, for schema-per-tenant layouts. Each schema
  produces distinct SQL text and therefore a distinct entry in pgx's statement
  cache; this is documented, not tuned.

## Not provided

- `COPY TO`, LISTEN/NOTIFY and large objects are not part of the runtime; pgx
  offers them directly.

## Telemetry

Each terminal operation is one span; each relation loading step is a child
span. Attributes follow the OpenTelemetry database conventions (system,
operation, collection name). SQL arguments never go into spans.

## Performance gates

- No reflection in scanning; allocations per scanned row are benchmarked
  against hand-written `rows.Scan` on pgx, and the difference is part of the
  benchmark report.
- Every builder and loader function with logic has a benchmark next to its
  test.
