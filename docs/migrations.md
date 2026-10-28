# Migrations

ratel migrations are declarative: the schema files state the target, and the
engine plans how to get there. The key requirement, equal to correctness:
**a migration never locks a production database.** A step that needs a lock
blocking reads or writes waits for it at most `lock_timeout` (so it never sits
in the lock queue behind a long transaction) and holds it at most
`statement_timeout`. Work that scans or rewrites data never runs under such a
lock.

## Flow

```
schema files ─► apply to dev DB ─► render ─► desired rendering ─┐
                                                                ├─► diff ─► []Change ─► Assess ─► planner ─► steps ─► lint
migrations dir ─► apply to dev DB ─► render ─► current rendering ┘
                                                                   steps ─► ratel migrate apply ─► target DB
```

1. **Diff** compares two renderings, both normalized by PostgreSQL (each side
   is applied to its own database on the dev server and introspected as keyed
   DDL statements plus column and domain parts). Each change has a kind
   (`create index`, `set not null`, `alter column type`, `rename column`,
   ...) that the planner keys its recipes by, and the plain PostgreSQL that
   makes it. Renames and `USING` expressions of type changes come from
   `@ratel.renamed` and `@ratel.retype`; without a rename intent the diff
   reports drop plus create. An index or constraint dropped and created with
   the same definition under a new name is a rename. *Check:* an integration
   test applies the changes of each case to the current database on
   PostgreSQL 16, 17 and 18 and compares its rendering with the desired one.
2. **Assess** is a pure function `Assess(operations, env) Safety` over the
   operations of one statement.
3. **Planner** rewrites each change into safe steps and assigns phases.
4. **Lint** checks the steps and hand-written migration files.
5. **Apply** runs steps with guards and records each in the journal.

## Safety record

```go
type Safety struct {
	Locks    []RelLock // relation and lock mode, strictest first
	Effect   Effect    // Metadata | Scan | IndexBuild | Rewrite
	Tx       TxMode    // InTx | OwnStatement (cannot run in a transaction)
	Destroys Destroys  // None | Objects | Data
	Compat   Compat    // Safe | Conditional | Breaking
	Reasons  []string  // lint codes behind Effect, Destroys and Compat
	Waits    bool      // waits for older transactions (CONCURRENTLY builds, DETACH CONCURRENTLY)
}
```

- A statement is a list of operations (`create-index-concurrently` on
  `app.orders`, `add-foreign-key-not-valid` on `app.orders` referencing
  `app.users`, ...). The planner states the operations of every step it
  writes, so statements the parser's PostgreSQL 17 grammar cannot read
  (`NOT NULL ... NOT VALID` on 18) are still assessed; hand-written
  statements are classified from their parse tree, and an unknown statement
  is assessed as the worst case.
- The lock and effect table is data keyed by operation kind. *Check:* an
  integration test replays every entry against PostgreSQL 16, 17 and 18 and
  compares the observed `pg_locks` modes, `relfilenode` changes and waiting
  with the table. Statements that commit a first phase before waiting
  (`DETACH CONCURRENTLY`, `FINALIZE`) are checked against the locks they
  hold while waiting.
- `Env` carries the target major version (`postgres.version`), the existing
  relations and, per relation, whether it is partitioned, has a default
  partition, and its partition key columns; the relations created earlier in
  the plan; and the table of each existing index. Recipes are chosen by these
  facts.
- Rewrite detection for column changes (added columns, type, collation and
  generation changes): a probe on the dev database of the current state
  creates an empty temporary copy of the table (`LIKE ... INCLUDING ALL`),
  runs the same statement against it with `TimeZone = 'UTC'` in a
  transaction it rolls back, and compares the `relfilenode` of the table and
  of its indexes: a new table file is a rewrite, a new index file an index
  rebuild. Other rewrites come from the table. Every applied step also runs
  with `SET LOCAL TimeZone = 'UTC'`, so the probe and production see the same
  conversion.
- The same probe reads, from `pg_depend`, the objects using each view,
  routine and type, so a replacement knows what to recreate. Table and type
  renames run first in the probe's transaction, so the changes after them
  probe under the new names.
- Intents are read from the annotations of the desired catalog and keyed
  like the rendering: `@ratel.renamed` on a table, type or column,
  `@ratel.retype` and `@ratel.backfill` on a column, `@ratel.allow` on any
  object. A constraint or index renamed with the same definition needs no
  intent: the diff finds the rename.
- Columns are compared by name. A column a migration adds comes last in its
  table whatever its place in the schema files; diff and drift ignore column
  order.
- Tables created in the same plan are exempt from lock rules.

## Safe recipes

The planner emits the safe form whatever the table size seen at plan time:
dev data says nothing about production size. "Short DDL" and "long DDL" are
the step classes of [Apply guards](#apply-guards). A change kind without a
recipe that the planner cannot make safe fails the plan with its lint code
unless the schema object carries `@ratel.allow` for that code; then it is
emitted as short DDL under the lock guards.

**Indexes and constraints**

| Change | Planned steps |
|---|---|
| create index, plain table | `CREATE INDEX CONCURRENTLY` (own statement); on resume an invalid leftover is dropped concurrently and rebuilt |
| create index, partitioned table | `CREATE INDEX ... ON ONLY <parent>` (short DDL, index invalid), then per partition `CREATE INDEX CONCURRENTLY` and `ALTER INDEX <parent_idx> ATTACH PARTITION <part_idx>`; the parent index becomes valid when every partition is attached |
| drop index, plain table | `DROP INDEX CONCURRENTLY` |
| drop index, partitioned table | `DROP INDEX` as short DDL (metadata only; concurrent drop is not supported) |
| change index definition | new index under a temporary name by the create recipe, drop of the old one by the drop recipe, `ALTER INDEX ... RENAME` (short DDL) |
| change check, foreign key or unique constraint | the new one under a temporary name by the add recipe beside the old one; in contract the old one is dropped and the new one renamed (short DDL); a primary key is dropped and added again in contract, its index built in expand |
| rename index or constraint (same definition, new name) | `ALTER INDEX ... RENAME` or `RENAME CONSTRAINT` (short DDL) |
| add check | `ADD CONSTRAINT ... NOT VALID` (short DDL), then `VALIDATE CONSTRAINT` (long DDL) |
| add FK, plain table; partitioned table on 18+ | `ADD CONSTRAINT ... NOT VALID`, then `VALIDATE CONSTRAINT` |
| add FK, partitioned table on 16 and 17 | no safe recipe (PostgreSQL rejects `NOT VALID` there): `RTL-L04` |
| add unique or primary key | `CREATE UNIQUE INDEX CONCURRENTLY` (btree, default ordering, plain columns, not partial), then `ADD CONSTRAINT ... USING INDEX` (short DDL); a primary key first gets `NOT NULL` on its columns by the not-null recipe, because `USING INDEX` would otherwise scan under an exclusive lock |
| unique index, partitioned table | must include the partition key columns (else the schema is invalid); built by the partitioned index recipe |
| unique or primary key constraint, partitioned table | `USING INDEX` is not supported on partitioned tables, so the constraint builds its index under a lock: `RTL-L05`; a unique index serves the same uniqueness safely |
| set not null | 18+: `NOT NULL ... NOT VALID`, then validate; 16 and 17: `CHECK (col IS NOT NULL) NOT VALID`, validate, `SET NOT NULL` (skips the scan because of the validated check), drop the check |
| exclusion constraint on an existing table | no safe recipe: `RTL-L09` |
| drop constraint | short DDL in the contract phase |

**Columns**

| Change | Planned steps |
|---|---|
| add nullable column, column with a non-volatile default | short DDL |
| add column with a volatile default, or `NOT NULL` without default | nullable column with its default for new rows, backfill (`@ratel.backfill` required, batched by the primary key, else error), then the not-null recipe in contract, where old code no longer inserts without the column |
| add identity or stored generated column to an existing table | filled by a table rewrite: `RTL-R01` |
| change type or collation, no rewrite and no index rebuild (probe) | short DDL |
| change type with rewrite, or a change rebuilding indexes | expand: a new column of the new type, filled for new and changed rows by a trigger, and a copy of each index and constraint of the old column built over it without blocking; backfill; contract: in one short transaction, drop the trigger and the views, policies and triggers using the column, drop the old column, give the new one its name and the copies theirs, and create the users again (`@ratel.retype` required for a type change; `RTL-R02` or `RTL-R04` when the column is referenced by another table's foreign key or the table has no primary key) |
| rename | expand: a column under the new name, a trigger keeping both names in step both ways, backfill; contract, after every object using the old column is replaced: drop the trigger and the old column (`@ratel.renamed` required) |
| drop column | short DDL in the contract phase (metadata only) |
| set or drop default | short DDL |
| identity: add, set options, drop | short DDL; an identity added to a column the plan makes `NOT NULL` is added in contract, after it |
| change generated expression, `VIRTUAL` (18+) | short DDL |
| change generated expression, `STORED` | rewrites: handled as a type change with rewrite (new column) |
| drop a `STORED` expression | `DROP EXPRESSION` (short DDL; the values stay) |
| make a column generated, change its storage, or change its expression on 16 | drop and add the column (`RTL-D01`) |

**Tables, partitions, types, code objects**

| Change | Planned steps |
|---|---|
| create table, sequence, type, function, view | short DDL in expand |
| drop table, sequence, type | short DDL in the contract phase |
| alter sequence | short DDL |
| rename table | `ALTER TABLE ... RENAME TO` (short DDL, `@ratel.renamed` on the table), first in expand: every later change names the table by its new name; the intent is the consent to `RTL-C01` |
| set logged or unlogged | rewrites the table: `RTL-R03` |
| change partition key | no statement changes it: the table is dropped and created (`RTL-D01`) |
| move a partition to another bound or parent | detach by the detach recipe, then attach by the attach recipe |
| attach partition | a `CHECK` matching the bound, including `IS NOT NULL` of the key, is added `NOT VALID` and validated first (otherwise `ATTACH` scans under an exclusive lock), then `ATTACH` and the drop of the check as short DDL; a bound the check cannot express (several key columns, hash, `DEFAULT`) or a default partition, which is scanned under an exclusive lock: `RTL-L07` |
| create partition | `CREATE TABLE ... PARTITION OF` (short DDL); with a default partition the default is scanned under an exclusive lock: `RTL-L07` |
| partition's not-null constraint names | renamed while the table is detached: PostgreSQL refuses renaming inherited constraints |
| detach partition, no default partition | `DETACH PARTITION ... CONCURRENTLY` (long DDL), then the drop of the `CHECK` it leaves behind (short DDL); a partition moving to another bound or parent moves in expand, one becoming a plain table leaves in contract |
| detach partition, default partition exists | concurrent detach is not supported: `DETACH PARTITION` as short DDL |
| enum: add value | `ALTER TYPE ... ADD VALUE [BEFORE\|AFTER]` in its own step, committed before any step that uses the value (on 17+ not needed when the type is created in the same transaction) |
| rename type or domain | `ALTER TYPE` or `ALTER DOMAIN ... RENAME TO` (short DDL), first in expand like a table rename (`@ratel.renamed` on the type) |
| enum: rename value | `ALTER TYPE ... RENAME VALUE` (short DDL in expand, before the values added next to it; `@ratel.renamed` on the value is the consent to `RTL-C01`) |
| enum: remove or reorder values | PostgreSQL has no way to drop or move a value: `RTL-D05`; written as a hand-written migration |
| domain: set or drop default, drop not null, drop constraint | short DDL |
| domain: add check | `ALTER DOMAIN ... ADD CONSTRAINT ... NOT VALID` (short DDL), then `VALIDATE CONSTRAINT` (long DDL) |
| domain: set not null | scans every column of the domain: `RTL-L06` |
| domain over another base type, composite or range type change | dropped and created again with its users, as an incompatible view change |
| extension: new version or schema | `ALTER EXTENSION ... UPDATE TO` / `SET SCHEMA` (short DDL) |
| view, compatible change (columns only appended) | `CREATE OR REPLACE VIEW` (short DDL) |
| view, incompatible change, or a function whose signature or return type changes | drop and recreate the object and every view, function, policy and trigger using it, transitively (from `pg_depend`), in one transaction as short DDL; an object used by a table column, a default or a constraint has no recipe: `RTL-D01` |
| function body change, same signature | `CREATE OR REPLACE FUNCTION` (short DDL) |
| enable RLS | policies first, then `ENABLE ROW LEVEL SECURITY` |
| create, alter, drop policy; disable RLS | short DDL |
| grant, revoke | short DDL |

There is no shadow-table rewrite (`pg_repack` style). Rewrites go through
expand/contract or explicit permission.

## Phases

A plan has up to three phases, recorded per step:

- **expand** adds: new tables, columns, indexes, constraints not yet
  validated, sync triggers. Old and new application versions both work.
- **backfill** fills data in batches (`@ratel.backfill`): each `UPDATE` of
  the step runs over consecutive ranges of the batching key (the header's
  `key`, else the table's primary key, composite keys compared as rows), each
  range ending at the `migrate.backfill.batch`-th key after the last one, one
  transaction per batch under the short step timeouts, pausing
  `migrate.backfill.pause` between batches and while replica lag exceeds
  `max_replica_lag`. The `UPDATE` keeps its own `WHERE`; the range is added
  to it.
- **contract** removes and tightens: drops of old columns, tables, triggers,
  `SET NOT NULL`, validations that old code could violate.

`ratel migrate apply` runs expand and backfill. `ratel migrate contract` runs
contract; it is a separate command because only the operator knows when old
application versions are gone. Changes are placed by `Compat`: `Safe` goes to
expand; `Breaking` and every change with `Destroys != None` go to contract;
`Conditional` (constraint tightening that existing rows or old code may
violate) goes to contract, with its `NOT VALID` part in expand when the recipe
has one.

## Planned files

`ratel migrate plan <name>` writes one file
`<UTC timestamp YYYYMMDDHHMMSS>_<name>.sql` in the migrations directory; files
apply in name order. Each step is a block with a header:

```sql
-- ratel:step id=9c1e4a2b phase=expand tx=own class=long locks=app.orders:ShareUpdateExclusive pg=18
CREATE INDEX CONCURRENTLY orders_user_id_idx ON app.orders (user_id);
```

- Plans are static: reviewed in code review, checksummed in `ratel.sum`, and
  applied exactly as written. Apply never chooses a strategy by live table
  size.
- `ratel.sum` holds a hash per file and a hash of the whole directory;
  `ratel migrate verify` fails on any edit, reorder or missing file.
- A step id is the hash of the file name and the step's normalized SQL.
- Header fields: `id`; `phase` (`expand`, `backfill`, `contract`); `tx`
  (`in`, or `own` for a statement that cannot run in a transaction);
  `class` (`short`, `long`, `backfill`); `locks` (relation and mode, as
  `Safety` gives them); `pg` (the `postgres.version` planned for); `allow`
  (copied lint codes); `ops` (each operation as
  `kind:relation:other:rewrite`, so a step is assessed again without parsing
  its SQL, which may use syntax newer than the parser's grammar); `key` (the
  batching key of a backfill). Names in `locks`, `ops` and `key` are
  URL-query escaped.
- Statements are separated by `;` outside quotes, quoted identifiers,
  dollar quotes and comments; splitting needs no parser.
- A hand-written file may use headers without an `id`: a header starts a
  step holding the statements up to the next one, with the given fields. A
  statement outside any header is a step of its own in expand. Hand-written
  steps are classified from their parse tree; a statement the parser cannot
  read is assessed as the worst case.
- A step header carries every `@ratel.allow` that the planner copied from the
  schema object that caused the step. In hand-written files, `@ratel.allow`
  is written in the comment directly before the statement.
- There are no down migrations. Going back is a forward plan to an earlier
  schema, through the same assessment and lint.

## Apply guards

Every step runs on a connection with `application_name = 'ratel-migrate'`. The
run holds a session advisory lock on a dedicated connection, so two runs never
overlap: a second run fails at once instead of waiting. The key is the 64-bit
FNV-1a hash of the string `ratel.migrate` and the target database name.
Sessions named `ratel-migrate` are not counted by the pre-flight. A step
planned for a major version newer than the server's, or a server newer than
the latest ratel supports, is refused. Each SQL statement is sent as its own protocol
message; the runner itself issues `BEGIN`, `SET LOCAL` and `COMMIT`, so no
statement ever runs inside an implicit multi-statement transaction.

| Step class | Guards |
|---|---|
| short DDL (takes a blocking lock briefly) | pre-flight: no session holds a conflicting lock on the relation and no transaction is older than `long_tx_threshold`; otherwise wait without requesting the lock, polling on the retry schedule, bounded by `max_total_wait`; then `BEGIN; SET LOCAL lock_timeout; SET LOCAL statement_timeout; SET LOCAL TimeZone = 'UTC'; <ddl>; COMMIT`; on SQLSTATE `55P03` retry with the schedule and jitter, also bounded by `max_total_wait` |
| long DDL (`CONCURRENTLY`, `VALIDATE`, `DETACH CONCURRENTLY`) | takes at most `ShareUpdateExclusive`; same pre-flight on old transactions; `lock_timeout = 0` and `statement_timeout = 0` with the run's deadline; when a concurrent index build fails or is canceled, the invalid index it leaves is dropped concurrently |
| backfill | no DDL lock; batches as above |

- When retries are exhausted the step **fails** and prints the blocking
  sessions (pid, application, state, transaction age, query). There is no
  final attempt without a timeout.
- `transaction_timeout` (PostgreSQL 17+) bounds in-transaction steps: the
  attempt's `lock_timeout` plus `statement_timeout` per statement.
- The retry schedule `gitlab` is GitLab's `with_lock_retries` timing table
  (41 pairs of lock timeout and pause, repeated 15 times) without its final
  attempt without a timeout; every lock timeout is scaled by
  `lock_timeout / 100ms`, and pauses get ±25% jitter.
- Statements that cannot run in a transaction always run as their own step.

## Journal and resume

The first run creates schema `ratel` and the journal table:

```sql
CREATE TABLE ratel.migrations_journal (
	step_id     text PRIMARY KEY,
	file        text NOT NULL,
	checksum    text NOT NULL,
	phase       text NOT NULL CHECK (phase IN ('expand', 'backfill', 'contract')),
	started_at  timestamptz NOT NULL,
	finished_at timestamptz,
	outcome     text CHECK (outcome IN ('done', 'failed')),
	resume_key  text  -- backfill: last committed batch key
);
```

A rerun skips completed steps and checks catalog guards before repeating an
interrupted one:

| Interrupted step | Guard and action |
|---|---|
| concurrent index build | `pg_index.indisvalid = false`: drop the leftover concurrently, rebuild |
| validation | `pg_constraint.convalidated`: skip when already validated |
| concurrent detach | `pg_inherits.inhdetachpending = true`: `ALTER TABLE ... DETACH PARTITION ... FINALIZE` |
| short step | never half done: its journal row is marked done in the step's own transaction |
| backfill | resumes after `resume_key` (the statement and the last key of the last committed batch), which every committed batch updates in the same transaction |

A step recorded done whose statements changed since fails the run: an
applied migration file is never edited.

## Data changes

Hand-written migrations may change data only as backfill steps
(`-- ratel:step phase=backfill`): batched by primary key, one transaction per
batch, under the backfill guards. A set-based `UPDATE` or `DELETE` over a
whole table in any other step fails lint with `RTL-T06`.

## Lint

Rules have stable codes `RTL-<area><nn>`: `L` locks, `R` rewrites, `D`
destruction, `C` compatibility, `T` transaction structure, `P` practice.

- A finding the planner fixed is reported in the plan and does not fail CI.
- A finding it could not fix fails `ratel migrate lint` unless the step carries
  `@ratel.allow <code> reason=<text>`.
- Breaking changes in the expand phase fail lint the same way.
- Hand-written migration files are analysed with the same rules through the
  same `Safety` records; a planned step is checked for its transaction
  structure and for breaking changes in expand, its recipe being the
  planner's. `RTL-L01` is a step declared `class=long` (run without
  `lock_timeout`) that takes a blocking lock. `RTL-D03` is reported for
  planned type changes, whose old and new types the diff knows.
- `RTL-P01` and `RTL-P02` check the desired schema; `@ratel.allow` on the
  table or column allows them.

| Code | Finding |
|---|---|
| RTL-L01 | blocking lock on an existing table without lock-retry guard |
| RTL-L02 | non-concurrent index build on an existing table |
| RTL-L03 | non-concurrent index drop |
| RTL-L04 | validation scan under a blocking lock (check, FK) |
| RTL-L05 | unique or primary key built under an exclusive lock |
| RTL-L06 | `SET NOT NULL` scan |
| RTL-L07 | `ATTACH PARTITION` scan under an exclusive lock |
| RTL-L08 | blocking `DETACH PARTITION` |
| RTL-L09 | exclusion constraint on an existing table |
| RTL-L10 | non-concurrent `REINDEX` |
| RTL-R01 | rewrite from a volatile default, serial, identity or stored generated column |
| RTL-R02 | type change rewrites the table |
| RTL-R03 | rewrite by `SET LOGGED`, access method, tablespace, `CLUSTER`, `VACUUM FULL` |
| RTL-R04 | type or collation change rebuilds indexes |
| RTL-D01 | drops a table, column or schema |
| RTL-D02 | `TRUNCATE` |
| RTL-D03 | narrowing type change may lose data |
| RTL-D04 | drops an index, constraint or default; hidden `CASCADE` dependents |
| RTL-D05 | removes an enum value |
| RTL-C01 | rename visible to running code |
| RTL-C02 | drop of an object running code may still use |
| RTL-C03 | new `NOT NULL` without default |
| RTL-C04 | type change visible to prepared statements and scanners |
| RTL-C05 | new enum value reaches old clients |
| RTL-C06 | constraint tightening |
| RTL-C07 | dropped default or dropped `NOT NULL` |
| RTL-C08 | `ENABLE ROW LEVEL SECURITY` before policies |
| RTL-T01 | statement that cannot run in a transaction inside one |
| RTL-T02 | new enum value used in the same transaction (not reported on 17+ when the type is created in that transaction) |
| RTL-T03 | slow work after an exclusive lock in the same transaction |
| RTL-T04 | hand-written step overriding the runner's `lock_timeout` or `statement_timeout` |
| RTL-T05 | exclusive locks on several tables in one transaction |
| RTL-T06 | data change outside a backfill step |
| RTL-P01 | FK columns without a supporting index (also reported for ORM relations) |
| RTL-P02 | `int4` primary key, `serial`, `timestamp` without time zone, `char(n)` |

## Drift

`ratel migrate drift` renders the target and diffs it against the rendering
the applied migrations produce on the dev server. Any difference fails with the
list of changes.
