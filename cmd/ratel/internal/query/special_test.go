package query_test

import (
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testSpecial(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()

	for _, tc := range []inferenceCase{
		{
			name: "null test", sql: `SELECT manager_id IS NULL, score IS NOT NULL FROM public.users`,
			columns: "bool,bool", nullable: "00",
		},
		{
			name: "boolean", sql: `SELECT (manager_id=$1) IS TRUE, NOT (score=$2) FROM public.users`, params: "int8,int4",
			columns: "bool,bool", nullable: "00",
		},
		{
			name: "array index", sql: `SELECT (ARRAY[1,2])[$1]`, params: "int4",
			columns: "int4", nullable: "1",
		},
		{
			name: "array slice", sql: `SELECT (ARRAY[1,2])[$1:$2]`, params: "int4,int4",
			columns: "_int4", nullable: "1",
		},
		{
			name: "minmax", sql: `SELECT greatest(manager_id,0),least(score,0) FROM public.users`,
			columns: "int8,int4", nullable: "00",
		},
		{
			name: "sql values", sql: `SELECT current_date,current_timestamp,current_user`,
			columns: "date,timestamptz,name", nullable: "000",
		},
		{
			name: "collation", sql: `SELECT name COLLATE "C" FROM public.users`,
			columns: "text", nullable: "0",
		},
	} {
		runInference(t, parser, cat, signatures, tc)
	}
}

func benchmarkSpecial(b *testing.B) {
	b.Helper()
	benchmarkSQL(b, `SELECT greatest(score,$2),row_number() OVER(ORDER BY $1)
 FROM public.users GROUP BY score ORDER BY score`)
}
