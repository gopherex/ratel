package probe

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// dependentsQuery is, for each view, routine and user type, the objects with
// a normal dependency on it, keyed as introspection renders them. A user the
// planner cannot recreate is keyed by what it is: a table column, a column
// default, a constraint, or any other catalog object.
const dependentsQuery = `
WITH objects(key, classid, objid) AS (
  SELECT format('view %I.%I', n.nspname, c.relname), 'pg_catalog.pg_class'::regclass, c.oid
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('v', 'm') AND n.nspname = ANY($1)
  UNION ALL
  SELECT 'routine ' || p.oid::regprocedure::text, 'pg_catalog.pg_proc'::regclass, p.oid
  FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
  WHERE n.nspname = ANY($1)
  UNION ALL
  SELECT format('type %I.%I', n.nspname, t.typname), 'pg_catalog.pg_type'::regclass, t.oid
  FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
  WHERE t.typtype IN ('e', 'd', 'c', 'r') AND n.nspname = ANY($1)
), users AS (
  SELECT o.key, d.classid, d.objid, d.objsubid
  FROM objects o
  JOIN pg_catalog.pg_depend d ON d.refclassid = o.classid AND d.refobjid = o.objid AND d.deptype = 'n'
  WHERE NOT (d.classid = 'pg_catalog.pg_rewrite'::regclass
             AND (SELECT r.ev_class FROM pg_catalog.pg_rewrite r WHERE r.oid = d.objid) = o.objid)
    AND NOT (d.classid = 'pg_catalog.pg_type'::regclass
             AND (SELECT t.typtype FROM pg_catalog.pg_type t WHERE t.oid = d.objid) = 'b')
)
SELECT u.key, COALESCE(CASE u.classid
  WHEN 'pg_catalog.pg_rewrite'::regclass THEN
    (SELECT format('view %I.%I', n.nspname, c.relname) FROM pg_catalog.pg_rewrite r
     JOIN pg_catalog.pg_class c ON c.oid = r.ev_class JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE r.oid = u.objid)
  WHEN 'pg_catalog.pg_proc'::regclass THEN 'routine ' || u.objid::regprocedure::text
  WHEN 'pg_catalog.pg_policy'::regclass THEN
    (SELECT format('policy %I.%I.%I', n.nspname, c.relname, p.polname) FROM pg_catalog.pg_policy p
     JOIN pg_catalog.pg_class c ON c.oid = p.polrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE p.oid = u.objid)
  WHEN 'pg_catalog.pg_trigger'::regclass THEN
    (SELECT format('trigger %I.%I.%I', n.nspname, c.relname, t.tgname) FROM pg_catalog.pg_trigger t
     JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE t.oid = u.objid)
  WHEN 'pg_catalog.pg_class'::regclass THEN
    (SELECT format('column %I.%I.%I', n.nspname, c.relname, a.attname) FROM pg_catalog.pg_class c
     JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = u.objsubid
     WHERE c.oid = u.objid)
  WHEN 'pg_catalog.pg_attrdef'::regclass THEN
    (SELECT format('default %I.%I.%I', n.nspname, c.relname, a.attname) FROM pg_catalog.pg_attrdef ad
     JOIN pg_catalog.pg_class c ON c.oid = ad.adrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = ad.adnum
     WHERE ad.oid = u.objid)
  WHEN 'pg_catalog.pg_constraint'::regclass THEN
    (SELECT format('constraint %I.%I', n.nspname, k.conname) FROM pg_catalog.pg_constraint k
     JOIN pg_catalog.pg_namespace n ON n.oid = k.connamespace WHERE k.oid = u.objid)
  END, 'object ' || u.classid::regclass::text || ' ' || u.objid)
FROM users u
ORDER BY 1, 2`

func dependentsOf(ctx context.Context, transaction pgx.Tx, namespaces []string) (map[string][]string, error) {
	rows, err := transaction.Query(ctx, dependentsQuery, namespaces)
	if err != nil {
		return nil, fmt.Errorf("probe: dependents: %w", err)
	}

	defer rows.Close()

	result := make(map[string][]string)

	for rows.Next() {
		var object, user string
		if err := rows.Scan(&object, &user); err != nil {
			return nil, fmt.Errorf("probe: dependents: %w", err)
		}

		result[object] = append(result[object], user)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("probe: dependents: %w", err)
	}

	return result, nil
}
