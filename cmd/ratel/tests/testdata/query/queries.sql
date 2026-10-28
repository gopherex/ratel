-- name: UserByID :one
SELECT id, name, manager_id FROM public.users WHERE id = @id;

-- name: UserIDs :many
SELECT id FROM public.users WHERE id = ANY(@ids) LIMIT @limit OFFSET @offset;

-- name: JoinLeft :many
SELECT u.id, m.id FROM public.users u LEFT JOIN public.users m ON m.id=u.manager_id;

-- name: JoinFull :many
SELECT u.id, m.id FROM public.users u FULL JOIN public.users m ON m.id=u.manager_id;

-- name: CTE :many
WITH x AS (SELECT id FROM public.users WHERE score=$1) SELECT id FROM x;

-- name: Recursive :many
WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM x WHERE n<$1) SELECT n FROM x;

-- name: Subquery :one
SELECT (SELECT name FROM public.users WHERE id=$1) AS name;

-- name: Lateral :many
SELECT u.id, q.name FROM public.users u
LEFT JOIN LATERAL (SELECT name FROM public.users WHERE id=u.manager_id) q ON true;

-- name: UnionTypes :many
SELECT 1 AS n UNION ALL SELECT 2::bigint;

-- name: UnionNull :many
SELECT NULL AS n UNION ALL SELECT 1;

-- name: IntersectTypes :many
SELECT 1 INTERSECT SELECT 2::bigint;

-- name: ExceptTypes :many
SELECT 1 EXCEPT SELECT NULL::int;

-- name: ValueTypes :many
VALUES (1,'x'),(2,NULL);

-- name: Aggregates :one
SELECT count(*), sum(score) FROM public.users;

-- name: WindowTypes :many
SELECT id,row_number() OVER (PARTITION BY name ORDER BY id),lag(score) OVER (ORDER BY id)
FROM public.users;

-- name: FrameParam :many
SELECT sum(score) OVER (ORDER BY id ROWS BETWEEN $1 PRECEDING AND CURRENT ROW) FROM public.users;

-- name: Grouping :many
SELECT name,count(*) FROM public.users GROUP BY GROUPING SETS ((name),());

-- name: Rollup :many
SELECT name,score,count(*) FROM public.users GROUP BY ROLLUP(name,score);

-- name: Cube :many
SELECT name,score,count(*) FROM public.users GROUP BY CUBE(name,score);

-- name: FunctionTypes :one
SELECT abs(1), public.widen(1), length('hello'), make_interval(1), concat('id=',1);

-- name: JsonTypes :one
SELECT '{}'::jsonb ->> 'missing' AS value, jsonb_build_object('a',1);

-- name: JsonTable :many
SELECT key,value FROM jsonb_each($1::jsonb);

-- name: Ordinality :many
SELECT n,ord FROM unnest($1::int[]) WITH ORDINALITY AS t(n,ord);

-- name: CaseTypes :many
SELECT coalesce(manager_id,0),CASE WHEN score=$1 THEN name END FROM public.users;

-- name: InsertUser :one
INSERT INTO public.users(id,name,score) VALUES($1,$2,$3) RETURNING id,name;

-- name: UpsertUser :one
INSERT INTO public.users(id,name,score) VALUES($1,$2,$3)
ON CONFLICT(id) DO UPDATE SET name=$4 WHERE users.score>$5 RETURNING id;

-- name: UpdateUser :one
UPDATE public.users SET name=$1 WHERE id=$2 RETURNING id,name;

-- name: DeleteUser :one
DELETE FROM public.users WHERE id=$1 RETURNING id;

-- name: WriteCTE :many
WITH x AS (DELETE FROM public.users WHERE id=$1 RETURNING name) SELECT name FROM x;

-- name: InsertSelect :one
WITH x AS (SELECT $1::bigint AS id)
INSERT INTO public.users(id,name,score) SELECT id,$2,$3 FROM x RETURNING id;

-- name: NullResults :one
SELECT (SELECT name FROM public.users WHERE false), NULL::int,
       '{}'::jsonb ->> 'absent', nullif(1,1), (ARRAY[1])[2];

-- name: UsingColumns :many
SELECT id,m.name FROM public.users u LEFT JOIN public.users m USING(id);

-- name: AnyElement :one
SELECT array_append(ARRAY[1,2],3);

-- name: SQLValues :one
SELECT current_date,current_timestamp,current_user;

-- name: NamedLexical :many
SELECT '@literal', $$@dollar$$, id FROM public.users WHERE id=@real OR id=@real;

-- name: RecursiveNulls :many
WITH RECURSIVE x(a,b,n) AS (
    SELECT 1,2,0 UNION ALL SELECT b,NULL::int4,n+1 FROM x WHERE n<2
) SELECT a,b FROM x;

-- name: QualifiedOutput :many
SELECT id FROM users WHERE id=$1;

-- name: Arrays :one
SELECT ARRAY(SELECT id FROM public.users),greatest(1,2),least(1,2);

-- name: RecordDefinition :one
SELECT id,name FROM jsonb_to_record($1::jsonb) AS t(id int8,name text);

-- name: MultiGrouping :many
SELECT name,score FROM public.users GROUP BY GROUPING SETS ((name,score),());

-- name: RangeWindow :many
SELECT sum(score) OVER (ORDER BY score RANGE $1 PRECEDING) FROM public.users;

-- name: UserEnum :one
SELECT $1::public.status;

-- name: UserTableFunction :many
SELECT identifiers.id FROM public.identifiers($1);

-- name: FullUsing :many
SELECT id,m.* FROM public.users u FULL JOIN public.users m USING(id);

-- name: NullBuiltins :one
SELECT ntile(NULL::int4) OVER (), '{}'::jsonb @@ '$.missing'::jsonpath;

-- name: GroupQualified :many
SELECT u.name,grouping(u.name) FROM public.users u GROUP BY ROLLUP(u.name);

-- name: UnionParameter :many
SELECT $1 UNION ALL SELECT 1;

-- name: ValuesParameter :many
VALUES ($1),(1);

-- name: OperatorParameters :one
SELECT @a::int4+@b::int4, '{}'::jsonb@>@data::jsonb;

-- name: QuotedRoutines :one
SELECT "x.y".pick(1),x."y.pick"(1);

-- name: UserDefaults :one
SELECT public.with_default(1), public.tricky(1,2), public.tricky(1,'x');

-- name: UserVariadic :one
SELECT public.collect(1,2), public.collect(VARIADIC ARRAY[1,2]);

-- name: ViewIsolation :many
WITH users AS (SELECT 'fake'::text AS id) SELECT id FROM public.unqualified_people;

-- name: InsertIsolation :one
WITH x AS (SELECT 1::int8 AS id)
INSERT INTO public.users(id,name,score)
WITH x AS (SELECT 'fake'::text AS id) SELECT $1,$2,$3 RETURNING (SELECT id FROM x);

-- name: GroupAlias :many
SELECT name AS label,count(*) FROM public.users GROUP BY label;

-- name: DistinctAlias :many
SELECT DISTINCT ON(label) name AS label FROM public.users;

-- name: QualifiedOperator :one
SELECT 1 OPERATOR(pg_catalog.+) 2;

-- name: TargetOverloadOrder :many
SELECT abs($1) FROM public.users WHERE score=$1;

-- name: TargetCastOrder :many
SELECT $1::int4 FROM public.users WHERE id=$1;

-- name: ExplicitRecast :one
SELECT $1::text,$1::int4;

-- name: WindowContextOrder :many
SELECT sum(score) OVER(ORDER BY id ROWS $1 PRECEDING) FROM public.users WHERE score=$1;

-- name: OrderedAggregate :one
SELECT percentile_cont($1::float8) WITHIN GROUP(ORDER BY score) FROM public.users;

-- name: UnconstrainedTarget :one
SELECT $1;

-- name: ReturningParameter :one
UPDATE public.users SET score=1 RETURNING $1;

-- name: ArrayAssignment :one
UPDATE public.arrays SET items[$2]=$1 RETURNING items;

-- name: TupleAssignment :one
UPDATE public.users SET (name,score)=($1,$2) RETURNING id;

-- name: TupleSubquery :one
UPDATE public.users SET (name,score)=(SELECT $1::text,$2::int4) RETURNING id;

-- name: SimpleCase :one
SELECT CASE $1 WHEN 'one' THEN 1 ELSE 2 END;

-- name: DomainOverload :many
SELECT abs(value),public.widen(value) FROM public.money;

-- name: DomainCommon :many
SELECT coalesce(value,NULL),coalesce(value,value) FROM public.money;

-- name: ExistingRangeType :many
SELECT sum(score) OVER(ORDER BY score RANGE $1 PRECEDING) FROM public.users WHERE id=$1;

-- name: InheritedWindow :many
SELECT sum(score) OVER(w RANGE $1 PRECEDING) FROM public.users WINDOW w AS(ORDER BY score);

-- name: UnknownComparison :one
SELECT $1=$2;

-- name: WindowSort :one
SELECT row_number() OVER(ORDER BY $1);

-- name: WindowPartition :one
SELECT row_number() OVER(PARTITION BY $1);

-- name: AggregateSort :one
SELECT array_agg(score ORDER BY $1) FROM public.users;

-- name: SortParameter :many
SELECT id FROM public.users ORDER BY $1;

-- name: GroupParameter :many
SELECT count(*) FROM public.users GROUP BY $1;

-- name: DistinctParameter :many
SELECT DISTINCT ON($1) id FROM public.users;

-- name: CompositeFunction :many
SELECT * FROM public.user_rows();

-- name: PopulatedRecord :one
SELECT * FROM jsonb_populate_record(NULL::public.users,$1::jsonb);

-- name: ScalarAlias :many
SELECT * FROM unnest($1::int4[]) AS x;

-- name: InsertExpressionContext :one
INSERT INTO public.users(id,name,score) SELECT coalesce($1,1),$2,$3 RETURNING id;

-- name: InsertWhereContext :one
INSERT INTO public.users(id,name,score)
SELECT $1::int4,$2,$3 FROM public.users u WHERE u.score=$1 RETURNING id;
