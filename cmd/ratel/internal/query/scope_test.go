package query_test

import (
	"testing"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testScopes(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()

	for _, tc := range []inferenceCase{
		{
			name: "full join", sql: `SELECT u.id,m.id FROM public.users u FULL JOIN public.users m ON u.id=m.id`,
			columns: "int8,int8", nullable: "11",
		},
		{
			name: "right join", sql: `SELECT u.id,m.id FROM public.users u RIGHT JOIN public.users m ON u.id=m.id`,
			columns: "int8,int8", nullable: "10",
		},
		{
			name: "using", sql: `SELECT id FROM public.users u JOIN public.users m USING(id)`,
			columns: "int8", nullable: "0",
		},
		{
			name: "using qualified", sql: `SELECT m.id FROM public.users u LEFT JOIN public.users m USING(id)`,
			columns: "int8", nullable: "1",
		},
		{
			name: "cte subquery", sql: `WITH x AS (SELECT id FROM public.users)
 SELECT id FROM (SELECT id FROM x) q`,
			columns: "int8", nullable: "0",
		},
		{
			name: "group alias", sql: `SELECT name AS label,count(*) FROM public.users GROUP BY label`,
			columns: "text,int8", nullable: "00",
		},
		{
			name: "distinct alias", sql: `SELECT DISTINCT ON(label) name AS label FROM public.users`,
			columns: "text", nullable: "0",
		},
		{
			name: "sort parameter", sql: `SELECT id FROM public.users ORDER BY $1`, params: "text",
			columns: "int8", nullable: "0",
		},
		{
			name: "group parameter", sql: `SELECT count(*) FROM public.users GROUP BY $1`, params: "text",
			columns: "int8", nullable: "0",
		},
		{
			name: "distinct parameter", sql: `SELECT DISTINCT ON($1) id FROM public.users`,
			params:  "text",
			columns: "int8", nullable: "0",
		},
		{
			name: "grouping sets", sql: `SELECT name,count(*) FROM public.users GROUP BY GROUPING SETS ((name),())`,
			columns: "text,int8", nullable: "10",
		},
		{
			name: "star", sql: `SELECT u.* FROM public.users u`,
			columns: "int8,text,int8,int4", nullable: "0010",
		},
		{
			name: "using star", sql: `SELECT m.* FROM public.users u LEFT JOIN public.users m USING(id)`,
			columns: "int8,text,int8,int4", nullable: "1111",
		},
		{
			name: "full using", sql: `SELECT id FROM public.users u FULL JOIN public.users m USING(id)`,
			columns: "int8", nullable: "0",
		},
		{
			name: "function qualifier", sql: `SELECT identifiers.id FROM public.identifiers($1)`,
			params:  "int8",
			columns: "int8", nullable: "1",
		},
		{
			name: "view cte isolation", sql: `WITH users AS (SELECT 'fake'::text AS id)
 SELECT id FROM public.unqualified_people`,
			columns: "int8", nullable: "0",
		},
		{
			name: "composite function", sql: `SELECT * FROM public.user_rows()`,
			columns: "int8,text,int8,int4", nullable: "1111",
		},
		{
			name: "populated record", sql: `SELECT * FROM jsonb_populate_record(NULL::public.users,$1::jsonb)`,
			params:  "jsonb",
			columns: "int8,text,int8,int4", nullable: "1111",
		},
		{
			name: "scalar alias", sql: `SELECT * FROM unnest($1::int4[]) AS x`, params: "_int4",
			columns: "int4", nullable: "1",
		},
		{
			name: "view", sql: `SELECT id FROM public.people`,
			columns: "int8", nullable: "0",
		},
		{
			name: "record definition", sql: `SELECT id,name FROM jsonb_to_record($1::jsonb) AS t(id int8,name text)`,
			params:  "jsonb",
			columns: "int8,text", nullable: "11",
		},
		{
			name: "qualified grouping", sql: `SELECT u.name,grouping(u.name) FROM public.users u GROUP BY ROLLUP(u.name)`,
			columns: "text,int4", nullable: "10",
		},
		{
			name: "multi grouping", sql: `SELECT name,score FROM public.users GROUP BY GROUPING SETS ((name,score),())`,
			columns: "text,int4", nullable: "11",
		},
		{
			name: "record function", sql: `SELECT key,value FROM jsonb_each($1::jsonb)`, params: "jsonb",
			columns: "text,jsonb", nullable: "11",
		},
	} {
		runInference(t, parser, cat, signatures, tc)
	}
}

func benchmarkScopes(b *testing.B) {
	b.Helper()
	benchmarkSQL(b, `SELECT u.id,m.name FROM public.users u LEFT JOIN public.users m ON m.id=u.manager_id`)
}
