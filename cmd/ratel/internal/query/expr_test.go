package query_test

import (
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testExpressions(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()

	for _, tc := range []inferenceCase{
		{
			name: "case param", sql: `SELECT CASE WHEN $1 THEN $2 ELSE 1 END`,
			params: "bool,int4", columns: "int4", nullable: "0",
		},
		{
			name: "coalesce param", sql: `SELECT coalesce($1, name) FROM public.users`,
			params: "text", columns: "text", nullable: "0",
		},
		{
			name: "in", sql: `SELECT id FROM public.users WHERE id IN ($1,$2)`,
			params: "int8,int8", columns: "int8", nullable: "0",
		},
		{
			name: "between", sql: `SELECT id FROM public.users WHERE score BETWEEN $1 AND $2`,
			params: "int4,int4", columns: "int8", nullable: "0",
		},
		{name: "nullif", sql: `SELECT nullif(score,0) FROM public.users`, columns: "int4", nullable: "1"},
		{name: "unknown comparison", sql: `SELECT $1=$2`, params: "text,text", columns: "bool", nullable: "0"},
		{name: "window sort", sql: `SELECT row_number() OVER(ORDER BY $1)`, params: "text", columns: "int8", nullable: "0"},
		{
			name: "window partition", sql: `SELECT row_number() OVER(PARTITION BY $1)`,
			params: "text", columns: "int8", nullable: "0",
		},
		{
			name: "aggregate sort", sql: `SELECT array_agg(score ORDER BY $1) FROM public.users`,
			params: "text", columns: "_int4", nullable: "1",
		},
		{name: "range existing type", sql: `SELECT sum(score) OVER(ORDER BY score RANGE $1 PRECEDING)
 FROM public.users WHERE id=$1`, params: "int8", columns: "int8", nullable: "1"},
		{name: "inherited window", sql: `SELECT sum(score) OVER(w RANGE $1 PRECEDING)
 FROM public.users WINDOW w AS(ORDER BY score)`, params: "int4", columns: "int8", nullable: "1"},
		{
			name: "simple case", sql: `SELECT CASE $1 WHEN 'one' THEN 1 ELSE 2 END`,
			params: "text", columns: "int4", nullable: "0",
		},
		{
			name: "target overload order", sql: `SELECT abs($1) FROM public.users WHERE score=$1`,
			params: "float8", columns: "float8", nullable: "0",
		},
		{
			name: "target cast order", sql: `SELECT $1::int4 FROM public.users WHERE id=$1`,
			params: "int4", columns: "int4", nullable: "0",
		},
		{name: "explicit recast", sql: `SELECT $1::text,$1::int4`, params: "text", columns: "text,int4", nullable: "00"},
		{name: "window context order", sql: `SELECT sum(score) OVER(ORDER BY id ROWS $1 PRECEDING)
 FROM public.users WHERE score=$1`, params: "int4", columns: "int8", nullable: "1"},
		{
			name: "ordered aggregate", sql: `SELECT percentile_cont($1::float8) WITHIN GROUP(ORDER BY score) FROM public.users`,
			params: "float8", columns: "float8", nullable: "1",
		},
		{name: "qualified operator", sql: `SELECT 1 OPERATOR(pg_catalog.+) 2`, columns: "int4", nullable: "0"},
		{name: "named arithmetic", sql: `SELECT @a::int4+@b::int4`, params: "int4,int4", columns: "int4", nullable: "0"},
		{name: "named json", sql: `SELECT '{}'::jsonb@>@data::jsonb`, params: "jsonb", columns: "bool", nullable: "0"},
		{name: "nullable ntile", sql: `SELECT ntile(NULL::int4) OVER ()`, columns: "int4", nullable: "1"},
		{name: "json predicate", sql: `SELECT '{}'::jsonb @@ '$.missing'::jsonpath`, columns: "bool", nullable: "1"},
		{
			name: "range window", sql: `SELECT sum(score) OVER (ORDER BY score RANGE $1 PRECEDING) FROM public.users`,
			params: "int4", columns: "int8", nullable: "1",
		},
		{
			name: "is distinct", sql: `SELECT manager_id IS DISTINCT FROM $1 FROM public.users`,
			params: "int8", columns: "bool", nullable: "0",
		},
		{
			name: "array subquery", sql: `SELECT ARRAY(SELECT id FROM public.users WHERE score=$1)`,
			params: "int4", columns: "_int8", nullable: "0",
		},
		{
			name: "exists", sql: `SELECT EXISTS(SELECT id FROM public.users WHERE score=$1)`,
			params: "int4", columns: "bool", nullable: "0",
		},
		{
			name: "in subquery", sql: `SELECT id IN (SELECT manager_id FROM public.users WHERE score=$1)
 FROM public.users`,
			params: "int4", columns: "bool", nullable: "1",
		},
		{
			name: "named lexical", sql: `SELECT '@fake', $$@dollar$$, id FROM public.users 
 WHERE id=@real OR id=@real`,
			params: "int8", columns: "text,text,int8", nullable: "000",
		},
	} {
		runInference(t, parser, cat, signatures, tc)
	}
}

func benchmarkExpressions(b *testing.B) {
	b.Helper()
	benchmarkSQL(b, `SELECT coalesce(manager_id,$1), CASE WHEN score=$2 THEN name END FROM public.users`)
}
