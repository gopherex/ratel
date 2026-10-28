package introspect

// section renders one object kind; its query returns one statement per row in
// a deterministic order. Sections follow creation dependencies, so the text
// also applies to an empty database once function bodies are not checked
// (check_function_bodies = off, as pg_dump does). A namespaced query takes
// the namespace array as $1; database-wide objects take no argument. Clauses
// that equal PostgreSQL
// defaults are omitted, so the rendered DDL maps to the same IR as
// hand-written DDL that leaves them out.
type section struct {
	name     string
	query    string
	database bool
}

// Every query skips objects created by an extension, which belong to the
// extension, and objects with an internal dependency (range constructors,
// identity sequences, partition clones), which their owner creates.
func sections() []section {
	return []section{
		{name: "schemas", query: schemas},
		{name: "extensions", query: extensions, database: true},
		{name: "types", query: types},
		{name: "routines", query: routines},
		{name: "sequences", query: sequences},
		{name: "tables", query: tables},
		{name: "constraints", query: constraints},
		{name: "row security", query: rowSecurity},
		{name: "views", query: views},
		{name: "indexes", query: indexes},
		{name: "foreign keys", query: foreignKeys},
		{name: "triggers", query: triggers},
		{name: "policies", query: policies},
		{name: "comments", query: comments},
		{name: "grants", query: grants},
	}
}

const schemas = `
SELECT format('CREATE SCHEMA %I;', n.nspname)
FROM pg_catalog.pg_namespace n
WHERE n.nspname = ANY($1) AND n.nspname <> 'public'
ORDER BY n.nspname`

const extensions = `
SELECT format('CREATE EXTENSION %I WITH SCHEMA %I VERSION %L;', e.extname, n.nspname, e.extversion)
FROM pg_catalog.pg_extension e
JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
WHERE e.extname <> 'plpgsql'
ORDER BY e.extname`

// User types depend on each other (a composite field of a domain, a domain
// over an enum), so all kinds render in one section ordered by the depth of
// their type dependencies.
const types = `
WITH RECURSIVE edges AS (
  SELECT CASE WHEN d.classid = 'pg_catalog.pg_type'::regclass THEN d.objid
              ELSE (SELECT t.oid FROM pg_catalog.pg_type t WHERE t.typrelid = d.objid) END AS dependent,
         d.refobjid AS dependency
  FROM pg_catalog.pg_depend d
  WHERE d.refclassid = 'pg_catalog.pg_type'::regclass AND d.deptype = 'n'
    AND (d.classid = 'pg_catalog.pg_type'::regclass
         OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c WHERE c.oid = d.objid AND c.relkind = 'c'))
), depth AS (
  SELECT t.oid, 0 AS level FROM pg_catalog.pg_type t
  UNION ALL
  SELECT e.dependent, depth.level + 1 FROM edges e JOIN depth ON depth.oid = e.dependency
  WHERE depth.level < 64
), rendered(oid, statement) AS (
  (` + enums + `) UNION ALL (` + composites + `)
  UNION ALL (` + domains + `) UNION ALL (` + ranges + `))
SELECT r.statement FROM rendered r
ORDER BY (SELECT max(depth.level) FROM depth WHERE depth.oid = r.oid), r.statement`

const enums = `
SELECT t.oid, format('CREATE TYPE %I.%I AS ENUM (%s);', n.nspname, t.typname,
         string_agg(quote_literal(e.enumlabel), ', ' ORDER BY e.enumsortorder))
FROM pg_catalog.pg_type t
JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
JOIN pg_catalog.pg_enum e ON e.enumtypid = t.oid
WHERE n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_type'::regclass AND d.objid = t.oid AND d.deptype IN ('e', 'i'))
GROUP BY t.oid, n.nspname, t.typname
ORDER BY n.nspname, t.typname`

const composites = `
SELECT t.oid, format('CREATE TYPE %I.%I AS (%s);', n.nspname, t.typname,
         string_agg(format('%I %s%s', a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod),
           CASE WHEN a.attcollation <> at.typcollation
             THEN ' COLLATE ' || a.attcollation::regcollation::text ELSE '' END),
           ', ' ORDER BY a.attnum))
FROM pg_catalog.pg_type t
JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
JOIN pg_catalog.pg_class c ON c.oid = t.typrelid AND c.relkind = 'c'
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
JOIN pg_catalog.pg_type at ON at.oid = a.atttypid
WHERE n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_type'::regclass AND d.objid = t.oid AND d.deptype IN ('e', 'i'))
GROUP BY t.oid, n.nspname, t.typname
ORDER BY n.nspname, t.typname`

const domains = `
SELECT t.oid, format('CREATE DOMAIN %I.%I AS %s%s%s%s%s;', n.nspname, t.typname,
         pg_catalog.format_type(t.typbasetype, t.typtypmod),
         CASE WHEN t.typcollation <> bt.typcollation
           THEN ' COLLATE ' || t.typcollation::regcollation::text ELSE '' END,
         CASE WHEN t.typdefaultbin IS NOT NULL
           THEN ' DEFAULT ' || pg_catalog.pg_get_expr(t.typdefaultbin, 0) ELSE '' END,
         CASE WHEN t.typnotnull THEN ' NOT NULL' ELSE '' END,
         COALESCE((SELECT string_agg(format(' CONSTRAINT %I %s', c.conname, pg_catalog.pg_get_constraintdef(c.oid)),
                    '' ORDER BY c.conname)
                   FROM pg_catalog.pg_constraint c WHERE c.contypid = t.oid AND c.contype = 'c'), ''))
FROM pg_catalog.pg_type t
JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
JOIN pg_catalog.pg_type bt ON bt.oid = t.typbasetype
WHERE t.typtype = 'd' AND n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_type'::regclass AND d.objid = t.oid AND d.deptype IN ('e', 'i'))
ORDER BY n.nspname, t.typname`

// The multirange name is rendered only when it differs from the one
// PostgreSQL derives: the first "range" replaced by "multirange", otherwise
// the name with a "_multirange" suffix.
const ranges = `
SELECT t.oid, format('CREATE TYPE %I.%I AS RANGE (subtype = %s%s%s%s%s%s);', n.nspname, t.typname,
         pg_catalog.format_type(r.rngsubtype, NULL),
         CASE WHEN NOT oc.opcdefault
           THEN format(', subtype_opclass = %I.%I', ocn.nspname, oc.opcname) ELSE '' END,
         CASE WHEN r.rngcollation <> 0 AND r.rngcollation <> st.typcollation
           THEN ', collation = ' || r.rngcollation::regcollation::text ELSE '' END,
         CASE WHEN r.rngcanonical <> 0 THEN ', canonical = ' || r.rngcanonical::regproc::text ELSE '' END,
         CASE WHEN r.rngsubdiff <> 0 THEN ', subtype_diff = ' || r.rngsubdiff::regproc::text ELSE '' END,
         CASE WHEN mt.typname <> CASE WHEN t.typname LIKE '%range%'
                                   THEN regexp_replace(t.typname, 'range', 'multirange')
                                   ELSE t.typname || '_multirange' END
           THEN format(', multirange_type_name = %I.%I', mn.nspname, mt.typname) ELSE '' END)
FROM pg_catalog.pg_range r
JOIN pg_catalog.pg_type t ON t.oid = r.rngtypid
JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
JOIN pg_catalog.pg_type st ON st.oid = r.rngsubtype
JOIN pg_catalog.pg_opclass oc ON oc.oid = r.rngsubopc
JOIN pg_catalog.pg_namespace ocn ON ocn.oid = oc.opcnamespace
JOIN pg_catalog.pg_type mt ON mt.oid = r.rngmultitypid
JOIN pg_catalog.pg_namespace mn ON mn.oid = mt.typnamespace
WHERE n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_type'::regclass AND d.objid = t.oid AND d.deptype IN ('e', 'i'))
ORDER BY n.nspname, t.typname`

// serials finds sequences that are exactly what a serial column creates: the
// default name and options, owned by the column, which defaults to nextval.
// They render as the serial column; any other owned sequence renders itself.
const serials = `serials AS (
  SELECT d.objid AS sequence, d.refobjid AS relation, d.refobjsubid AS attnum,
         CASE a.atttypid WHEN 'pg_catalog.int2'::regtype THEN 'smallserial'
                         WHEN 'pg_catalog.int4'::regtype THEN 'serial' ELSE 'bigserial' END AS type
  FROM pg_catalog.pg_depend d
  JOIN pg_catalog.pg_class sc ON sc.oid = d.objid AND sc.relkind = 'S'
  JOIN pg_catalog.pg_sequence s ON s.seqrelid = sc.oid
  JOIN pg_catalog.pg_class tc ON tc.oid = d.refobjid
  JOIN pg_catalog.pg_attribute a ON a.attrelid = tc.oid AND a.attnum = d.refobjsubid
  JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
  WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.refclassid = 'pg_catalog.pg_class'::regclass
    AND d.deptype = 'a' AND s.seqtypid = a.atttypid
    AND a.atttypid IN ('pg_catalog.int2'::regtype, 'pg_catalog.int4'::regtype, 'pg_catalog.int8'::regtype)
    AND sc.relnamespace = tc.relnamespace AND sc.relname = tc.relname || '_' || a.attname || '_seq'
    AND s.seqstart = 1 AND s.seqincrement = 1 AND s.seqmin = 1 AND s.seqcache = 1 AND NOT s.seqcycle
    AND s.seqmax = CASE a.atttypid WHEN 'pg_catalog.int2'::regtype THEN 32767
                                   WHEN 'pg_catalog.int4'::regtype THEN 2147483647 ELSE 9223372036854775807 END
    AND pg_catalog.pg_get_expr(ad.adbin, ad.adrelid) = format('nextval(%L::regclass)', sc.oid::regclass::text)
)`

const sequences = `
WITH ` + serials + `
SELECT format('CREATE SEQUENCE %I.%I AS %s INCREMENT BY %s MINVALUE %s MAXVALUE %s START WITH %s CACHE %s%s%s;',
         n.nspname, c.relname, pg_catalog.format_type(s.seqtypid, NULL),
         s.seqincrement, s.seqmin, s.seqmax, s.seqstart, s.seqcache,
         CASE WHEN s.seqcycle THEN ' CYCLE' ELSE '' END,
         COALESCE((SELECT format(' OWNED BY %I.%I.%I', tn.nspname, tc.relname, a.attname)
                   FROM pg_catalog.pg_depend d
                   JOIN pg_catalog.pg_class tc ON tc.oid = d.refobjid
                   JOIN pg_catalog.pg_namespace tn ON tn.oid = tc.relnamespace
                   JOIN pg_catalog.pg_attribute a ON a.attrelid = tc.oid AND a.attnum = d.refobjsubid
                   WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid
                     AND d.refclassid = 'pg_catalog.pg_class'::regclass AND d.deptype = 'a'), ''))
FROM pg_catalog.pg_sequence s
JOIN pg_catalog.pg_class c ON c.oid = s.seqrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = ANY($1) AND NOT EXISTS (SELECT 1 FROM serials WHERE serials.sequence = c.oid)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('e', 'i'))
ORDER BY n.nspname, c.relname`

// A PostgreSQL 18 not-null constraint keeps its name; 16 and 17 store only
// attnotnull, and the builder synthesizes the name.
const tables = `
WITH ` + serials + `
SELECT CASE WHEN c.relispartition
  THEN format('CREATE TABLE %I.%I PARTITION OF %s %s%s;', n.nspname, c.relname,
         (SELECT i.inhparent::regclass::text FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid),
         pg_catalog.pg_get_expr(c.relpartbound, c.oid),
         CASE WHEN c.relkind = 'p' THEN ' PARTITION BY ' || pg_catalog.pg_get_partkeydef(c.oid) ELSE '' END)
  ELSE format('CREATE %sTABLE %I.%I (%s)%s;',
         CASE c.relpersistence WHEN 'u' THEN 'UNLOGGED ' ELSE '' END, n.nspname, c.relname,
         COALESCE((SELECT string_agg(format('%I %s%s%s%s%s', a.attname,
             COALESCE(sr.type, pg_catalog.format_type(a.atttypid, a.atttypmod)),
             CASE WHEN a.attcollation <> at.typcollation
               THEN ' COLLATE ' || a.attcollation::regcollation::text ELSE '' END,
             CASE WHEN a.attidentity IN ('a', 'd')
               THEN (SELECT format(' GENERATED %s AS IDENTITY '
                                   '(START WITH %s INCREMENT BY %s MINVALUE %s MAXVALUE %s CACHE %s%s)',
                              CASE a.attidentity WHEN 'a' THEN 'ALWAYS' ELSE 'BY DEFAULT' END,
                              s.seqstart, s.seqincrement, s.seqmin, s.seqmax, s.seqcache,
                              CASE WHEN s.seqcycle THEN ' CYCLE' ELSE '' END)
                     FROM pg_catalog.pg_sequence s
                     WHERE s.seqrelid = pg_catalog.pg_get_serial_sequence(
                       format('%I.%I', n.nspname, c.relname), a.attname)::regclass)
               ELSE '' END,
             CASE
               WHEN a.attgenerated = 's'
                 THEN ' GENERATED ALWAYS AS (' || pg_catalog.pg_get_expr(ad.adbin, ad.adrelid) || ') STORED'
               WHEN a.attgenerated = 'v'
                 THEN ' GENERATED ALWAYS AS (' || pg_catalog.pg_get_expr(ad.adbin, ad.adrelid) || ') VIRTUAL'
               WHEN ad.adbin IS NOT NULL AND sr.type IS NULL
                 THEN ' DEFAULT ' || pg_catalog.pg_get_expr(ad.adbin, ad.adrelid)
               ELSE '' END,
             CASE WHEN NOT a.attnotnull THEN ''
               ELSE COALESCE((SELECT format(' CONSTRAINT %I NOT NULL', nn.conname)
                              FROM pg_catalog.pg_constraint nn
                              WHERE nn.conrelid = c.oid AND nn.contype = 'n' AND nn.conkey = ARRAY[a.attnum]),
                             ' NOT NULL') END),
             ', ' ORDER BY a.attnum)
           FROM pg_catalog.pg_attribute a
           JOIN pg_catalog.pg_type at ON at.oid = a.atttypid
           LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
           LEFT JOIN serials sr ON sr.relation = a.attrelid AND sr.attnum = a.attnum
           WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped), ''),
         CASE WHEN c.relkind = 'p' THEN ' PARTITION BY ' || pg_catalog.pg_get_partkeydef(c.oid) ELSE '' END)
  END
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p') AND n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY n.nspname, c.relname`

// Constraints cloned onto partitions from their parent are the parent's.
// Foreign keys follow indexes: they may reference a unique index.
const constraints = `
SELECT format('ALTER TABLE %I.%I ADD CONSTRAINT %I %s;', n.nspname, c.relname, k.conname,
         pg_catalog.pg_get_constraintdef(k.oid))
FROM pg_catalog.pg_constraint k
JOIN pg_catalog.pg_class c ON c.oid = k.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE k.contype IN ('p', 'u', 'c', 'x') AND k.conparentid = 0 AND k.conislocal
  AND c.relkind IN ('r', 'p') AND n.nspname = ANY($1)
ORDER BY n.nspname, c.relname, k.conname`

const foreignKeys = `
SELECT format('ALTER TABLE %I.%I ADD CONSTRAINT %I %s;', n.nspname, c.relname, k.conname,
         pg_catalog.pg_get_constraintdef(k.oid))
FROM pg_catalog.pg_constraint k
JOIN pg_catalog.pg_class c ON c.oid = k.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE k.contype = 'f' AND k.conparentid = 0 AND k.conislocal
  AND c.relkind IN ('r', 'p') AND n.nspname = ANY($1)
ORDER BY n.nspname, c.relname, k.conname`

const rowSecurity = `
SELECT format('ALTER TABLE %I.%I %s;', n.nspname, c.relname,
         concat_ws(', ',
           CASE WHEN c.relrowsecurity THEN 'ENABLE ROW LEVEL SECURITY' END,
           CASE WHEN c.relforcerowsecurity THEN 'FORCE ROW LEVEL SECURITY' END))
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p') AND (c.relrowsecurity OR c.relforcerowsecurity) AND n.nspname = ANY($1)
ORDER BY n.nspname, c.relname`

// Indexes backing a constraint are the constraint; partition indexes attached
// to a parent index are the parent's.
const indexes = `
SELECT pg_catalog.pg_get_indexdef(i.indexrelid) || ';'
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
JOIN pg_catalog.pg_class tc ON tc.oid = i.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid = tc.relnamespace
WHERE tc.relkind IN ('r', 'p', 'm') AND n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits inh WHERE inh.inhrelid = i.indexrelid)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k WHERE k.conindid = i.indexrelid)
ORDER BY n.nspname, tc.relname, ic.relname`

// Views are ordered by the depth of their dependencies on other views.
const views = `
WITH RECURSIVE edges AS (
  SELECT DISTINCT r.ev_class AS view, d.refobjid AS dependency
  FROM pg_catalog.pg_rewrite r
  JOIN pg_catalog.pg_depend d ON d.classid = 'pg_catalog.pg_rewrite'::regclass AND d.objid = r.oid
  JOIN pg_catalog.pg_class dc ON dc.oid = d.refobjid AND dc.relkind IN ('v', 'm')
  WHERE d.refobjid <> r.ev_class
), depth AS (
  SELECT c.oid, 0 AS level FROM pg_catalog.pg_class c WHERE c.relkind IN ('v', 'm')
  UNION ALL
  SELECT e.view, depth.level + 1 FROM edges e JOIN depth ON depth.oid = e.dependency
)
SELECT CASE c.relkind
  WHEN 'v' THEN format('CREATE VIEW %I.%I%s AS %s;', n.nspname, c.relname,
    CASE WHEN c.reloptions IS NOT NULL THEN ' WITH (' || array_to_string(c.reloptions, ', ') || ')' ELSE '' END,
    rtrim(pg_catalog.pg_get_viewdef(c.oid, true), ';'))
  ELSE format('CREATE MATERIALIZED VIEW %I.%I AS %s WITH %sDATA;', n.nspname, c.relname,
    rtrim(pg_catalog.pg_get_viewdef(c.oid, true), ';'),
    CASE WHEN c.relispopulated THEN '' ELSE 'NO ' END)
  END
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm') AND n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY (SELECT max(depth.level) FROM depth WHERE depth.oid = c.oid), n.nspname, c.relname`

const routines = `
SELECT pg_catalog.pg_get_functiondef(p.oid) || ';'
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE p.prokind IN ('f', 'p') AND n.nspname = ANY($1)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_proc'::regclass AND d.objid = p.oid AND d.deptype IN ('e', 'i'))
ORDER BY n.nspname, p.proname, p.oid::regprocedure::text`

// Triggers cloned onto partitions belong to the parent's trigger.
const triggers = `
SELECT pg_catalog.pg_get_triggerdef(t.oid) || ';'
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE NOT t.tgisinternal AND t.tgparentid = 0 AND n.nspname = ANY($1)
ORDER BY n.nspname, c.relname, t.tgname`

const policies = `
SELECT format('CREATE POLICY %I ON %I.%I%s%s%s%s%s;', p.polname, n.nspname, c.relname,
         CASE WHEN p.polpermissive THEN '' ELSE ' AS RESTRICTIVE' END,
         CASE p.polcmd WHEN 'r' THEN ' FOR SELECT' WHEN 'a' THEN ' FOR INSERT'
           WHEN 'w' THEN ' FOR UPDATE' WHEN 'd' THEN ' FOR DELETE' ELSE '' END,
         CASE WHEN p.polroles = '{0}' THEN ''
           ELSE ' TO ' || (SELECT string_agg(quote_ident(r.rolname), ', ' ORDER BY r.rolname)
                           FROM pg_catalog.pg_roles r WHERE r.oid = ANY(p.polroles)) END,
         CASE WHEN p.polqual IS NOT NULL
           THEN ' USING (' || pg_catalog.pg_get_expr(p.polqual, p.polrelid) || ')' ELSE '' END,
         CASE WHEN p.polwithcheck IS NOT NULL
           THEN ' WITH CHECK (' || pg_catalog.pg_get_expr(p.polwithcheck, p.polrelid) || ')' ELSE '' END)
FROM pg_catalog.pg_policy p
JOIN pg_catalog.pg_class c ON c.oid = p.polrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = ANY($1)
ORDER BY n.nspname, c.relname, p.polname`

// pg_identify_object renders the identity in the syntax COMMENT ON accepts,
// including "name on table" for constraints, triggers and policies.
const comments = `
SELECT format('COMMENT ON %s %s IS %L;',
         CASE o.type WHEN 'table column' THEN 'COLUMN' WHEN 'table constraint' THEN 'CONSTRAINT'
           ELSE upper(o.type) END,
         o.identity, d.description)
FROM pg_catalog.pg_description d
CROSS JOIN LATERAL pg_catalog.pg_identify_object(d.classoid, d.objoid, d.objsubid) o
WHERE (o.schema = ANY($1) OR (o.type = 'schema' AND o.identity = ANY($1))
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t
         JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
         JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
         WHERE d.classoid = 'pg_catalog.pg_trigger'::regclass AND t.oid = d.objoid AND n.nspname = ANY($1))
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_policy p
         JOIN pg_catalog.pg_class c ON c.oid = p.polrelid
         JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
         WHERE d.classoid = 'pg_catalog.pg_policy'::regclass AND p.oid = d.objoid AND n.nspname = ANY($1)))
  AND o.type IN ('schema', 'table', 'view', 'materialized view', 'sequence', 'table column', 'index',
                 'type', 'domain', 'function', 'procedure', 'trigger', 'policy', 'table constraint')
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend e
    WHERE e.classid = d.classoid AND e.objid = d.objoid AND e.deptype = 'e')
ORDER BY 1`

// The owner's privileges and the defaults PostgreSQL grants every object of
// a kind (acldefault: EXECUTE on routines and USAGE on types to PUBLIC) are
// not grants. Composite row types of tables carry no ACL of their own.
const grants = `
WITH acl AS (
  SELECT CASE WHEN c.relkind = 'S' THEN 'SEQUENCE' ELSE 'TABLE' END AS kind,
         format('%I.%I', n.nspname, c.relname) AS identity, c.relowner AS owner, c.relacl AS acl,
         pg_catalog.acldefault(CASE WHEN c.relkind = 'S' THEN 's' ELSE 'r' END::"char", c.relowner) AS defaults
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S') AND n.nspname = ANY($1)
  UNION ALL
  SELECT CASE WHEN t.typtype = 'd' THEN 'DOMAIN' ELSE 'TYPE' END,
         format('%I.%I', n.nspname, t.typname), t.typowner, t.typacl,
         pg_catalog.acldefault('T', t.typowner)
  FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
  WHERE t.typtype IN ('e', 'c', 'd', 'r') AND n.nspname = ANY($1)
    AND (t.typrelid = 0 OR EXISTS (SELECT 1 FROM pg_catalog.pg_class tc WHERE tc.oid = t.typrelid AND tc.relkind = 'c'))
  UNION ALL
  SELECT CASE WHEN p.prokind = 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,
         p.oid::regprocedure::text, p.proowner, p.proacl,
         pg_catalog.acldefault('f', p.proowner)
  FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
  WHERE p.prokind IN ('f', 'p') AND n.nspname = ANY($1)
  UNION ALL
  SELECT 'SCHEMA', format('%I', n.nspname), n.nspowner, n.nspacl,
         pg_catalog.acldefault('n', n.nspowner)
  FROM pg_catalog.pg_namespace n WHERE n.nspname = ANY($1)
), entries AS (
  SELECT acl.kind, acl.identity, x.grantee, x.is_grantable,
         string_agg(x.privilege_type, ', ' ORDER BY x.privilege_type) AS privileges
  FROM acl CROSS JOIN LATERAL aclexplode(acl.acl) x
  WHERE acl.acl IS NOT NULL AND x.grantee <> acl.owner
    AND NOT EXISTS (SELECT 1 FROM aclexplode(acl.defaults) d
      WHERE d.grantee = x.grantee AND d.privilege_type = x.privilege_type AND d.is_grantable = x.is_grantable)
  GROUP BY acl.kind, acl.identity, x.grantee, x.is_grantable
)
SELECT format('GRANT %s ON %s %s TO %s%s;', e.privileges, e.kind, e.identity,
         CASE WHEN e.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(r.rolname) END,
         CASE WHEN e.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END)
FROM entries e LEFT JOIN pg_catalog.pg_roles r ON r.oid = e.grantee
ORDER BY 1`
