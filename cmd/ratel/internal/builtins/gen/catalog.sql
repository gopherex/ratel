WITH type_refs AS (
 SELECT t.oid, t.typname, t.typcategory, t.typispreferred, t.typbasetype,
   CASE WHEN e.typarray = t.oid
        THEN e.typname || '[]' ELSE t.typname END AS signature,
   jsonb_strip_nulls(jsonb_build_object(
     'pg_name', t.typname,
     'kind', CASE WHEN e.typarray = t.oid THEN 2
                  WHEN t.typtype = 'p' THEN 7 WHEN t.typtype = 'c' THEN 5
                  WHEN t.typtype = 'd' THEN 4 WHEN t.typtype IN ('r', 'm') THEN 6 ELSE 1 END,
     'element', CASE WHEN e.typarray = t.oid
       THEN jsonb_build_object('pg_name', e.typname, 'kind',
         CASE e.typtype WHEN 'p' THEN 7 WHEN 'c' THEN 5 WHEN 'd' THEN 4
                        WHEN 'r' THEN 6 WHEN 'm' THEN 6 ELSE 1 END) END,
     'array_dimensions', CASE WHEN e.typarray = t.oid THEN 1 END
   )) AS ref
 FROM pg_catalog.pg_type t
 JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
 LEFT JOIN pg_catalog.pg_type e ON e.oid = t.typelem
 WHERE n.nspname = 'pg_catalog'
), routines AS (
 SELECT p.*, jsonb_build_object('parts', jsonb_build_array('pg_catalog', p.proname,
   COALESCE((SELECT string_agg(t.signature, ',' ORDER BY a.ordinality)
      FROM unnest(p.proargtypes::oid[]) WITH ORDINALITY a(oid, ordinality)
      JOIN type_refs t ON t.oid = a.oid), ''))) AS identity
 FROM pg_catalog.pg_proc p
 JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'pg_catalog' AND p.prokind <> 'p'
)
SELECT jsonb_build_object(
 'name', 'pg_catalog',
 'builtin_types', (SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
   'type', t.ref, 'category', t.typcategory, 'preferred', t.typispreferred,
   'base', b.ref, 'range_subtype', s.ref)))
   FROM type_refs t LEFT JOIN type_refs b ON b.oid = t.typbasetype
   LEFT JOIN pg_catalog.pg_range r ON r.rngtypid = t.oid OR r.rngmultitypid = t.oid
   LEFT JOIN type_refs s ON s.oid = r.rngsubtype),
 'functions', (SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
   'name', p.identity,
   'arguments', (SELECT jsonb_agg(jsonb_build_object(
     'name', COALESCE(p.proargnames[a.ordinality], ''), 'type', t.ref,
     'mode', CASE COALESCE(p.proargmodes[a.ordinality], 'i')
       WHEN 'o' THEN 2 WHEN 't' THEN 2 WHEN 'b' THEN 3 WHEN 'v' THEN 4 ELSE 1 END)
     ORDER BY a.ordinality)
     FROM unnest(COALESCE(p.proallargtypes, p.proargtypes::oid[])) WITH ORDINALITY a(oid, ordinality)
     JOIN type_refs t ON t.oid = a.oid),
   'returns', CASE WHEN p.proretset THEN jsonb_build_object('setof', jsonb_build_object('type', ret.ref))
                   ELSE jsonb_build_object('scalar', ret.ref) END,
   'volatility', CASE p.provolatile WHEN 'v' THEN 1 WHEN 's' THEN 2 ELSE 3 END,
   'null_input', CASE WHEN p.proisstrict THEN 2 ELSE 1 END,
   'routine_kind', p.prokind,
   'default_arguments', p.pronargdefaults,
   'variadic_element', variadic_type.ref,
   'aggregate', CASE WHEN agg.aggfnoid IS NOT NULL THEN jsonb_build_object(
     'kind', agg.aggkind, 'direct_arguments', agg.aggnumdirectargs) END)))
   FROM routines p JOIN type_refs ret ON ret.oid = p.prorettype
   LEFT JOIN type_refs variadic_type ON variadic_type.oid = p.provariadic
   LEFT JOIN pg_catalog.pg_aggregate agg ON agg.aggfnoid = p.oid),
 'builtin_operators', (SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
   'name', o.oprname, 'left', l.ref, 'right', r.ref, 'result', ret.ref, 'function', p.identity)))
   FROM pg_catalog.pg_operator o
   JOIN pg_catalog.pg_namespace n ON n.oid = o.oprnamespace AND n.nspname = 'pg_catalog'
   LEFT JOIN type_refs l ON l.oid = o.oprleft
   LEFT JOIN type_refs r ON r.oid = o.oprright
   JOIN type_refs ret ON ret.oid = o.oprresult JOIN routines p ON p.oid = o.oprcode),
 'builtin_casts', (SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
   'source', s.ref, 'target', t.ref, 'context', c.castcontext,
   'method', c.castmethod, 'function', p.identity)))
   FROM pg_catalog.pg_cast c JOIN type_refs s ON s.oid = c.castsource
   JOIN type_refs t ON t.oid = c.casttarget LEFT JOIN routines p ON p.oid = c.castfunc
   WHERE c.castcontext IN ('i', 'a'))
)
