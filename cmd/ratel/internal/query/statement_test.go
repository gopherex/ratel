package query_test

import (
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testStatements(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()

	for _, tc := range []inferenceCase{
		{name: "union null", sql: `SELECT NULL AS n UNION ALL SELECT 1`, columns: "int4", nullable: "1"},
		{name: "union parameter", sql: `SELECT $1 UNION ALL SELECT 1`, params: "int4", columns: "int4", nullable: "0"},
		{name: "values parameter", sql: `VALUES ($1),(1)`, params: "int4", columns: "int4", nullable: "0"},
		{
			name: "array assignment", sql: `UPDATE public.arrays SET items[$2]=$1 RETURNING items`,
			params: "int4,int4", columns: "_int4", nullable: "0",
		},
		{
			name: "tuple assignment", sql: `UPDATE public.users SET (name,score)=($1,$2) RETURNING id`,
			params: "text,int4", columns: "int8", nullable: "0",
		},
		{
			name: "tuple subquery", sql: `UPDATE public.users SET (name,score)=(SELECT $1::text,$2::int4) RETURNING id`,
			params: "text,int4", columns: "int8", nullable: "0",
		},
		{
			name: "returning parameter", sql: `UPDATE public.users SET score=1 RETURNING $1`,
			params: "text", columns: "text", nullable: "0",
		},
		{name: "insert expression context", sql: `INSERT INTO public.users(id,name,score)
 SELECT coalesce($1,1),$2,$3 RETURNING id`, params: "int4,text,int4", columns: "int8", nullable: "0"},
		{
			name: "insert where context", sql: `INSERT INTO public.users(id,name,score)
 SELECT $1::int4,$2,$3 FROM public.users u WHERE u.score=$1 RETURNING id`,
			params: "int4,text,int4", columns: "int8", nullable: "0",
		},
		{
			name: "insert cte isolation", sql: `WITH x AS (SELECT 1::int8 AS id)
 INSERT INTO public.users(id,name,score)
 WITH x AS (SELECT 'fake'::text AS id) SELECT $1,$2,$3 RETURNING (SELECT id FROM x)`,
			params: "int8,text,int4", columns: "int8", nullable: "1",
		},
		{
			name: "recursive nullability", sql: `WITH RECURSIVE x(a,b,n) AS (
 SELECT 1,2,0 UNION ALL SELECT b,NULL::int4,n+1 FROM x WHERE n<2) SELECT a,b FROM x`,
			columns: "int4,int4", nullable: "11",
		},
		{name: "intersect", sql: `SELECT 1 INTERSECT SELECT NULL::int4`, columns: "int4", nullable: "0"},
		{name: "except", sql: `SELECT 1 EXCEPT SELECT NULL::int4`, columns: "int4", nullable: "0"},
		{name: "merge missing source", sql: `MERGE INTO public.users u USING public.users m ON u.id=m.id
 WHEN NOT MATCHED BY SOURCE THEN DELETE RETURNING m.id`, columns: "int8", nullable: "1"},
		{
			name: "insert select", sql: `INSERT INTO public.users(id,name,score) SELECT $1,$2,$3 RETURNING id`,
			params: "int8,text,int4", columns: "int8", nullable: "0",
		},
		{
			name: "insert with", sql: `WITH x AS (SELECT $1::int8 AS id) 
INSERT INTO public.users(id,name,score) SELECT id,$2,$3 FROM x RETURNING id`,
			params: "int8,text,int4", columns: "int8", nullable: "0",
		},
		{
			name: "update from", sql: `UPDATE public.users u SET name=$1 
 FROM public.users m WHERE u.id=m.id AND m.score=$2 RETURNING u.id`,
			params: "text,int4", columns: "int8", nullable: "0",
		},
		{
			name: "merge", sql: `MERGE INTO public.users u USING public.users m ON u.id=m.id 
 WHEN MATCHED THEN UPDATE SET name=$1 RETURNING u.id`,
			params: "text", columns: "int8", nullable: "0",
		},
	} {
		runInference(t, parser, cat, signatures, tc)
	}
}

func benchmarkStatements(b *testing.B) {
	b.Helper()
	benchmarkSQL(b, `INSERT INTO public.users(id,name,score) VALUES($1,$2,$3) RETURNING id`)
}
