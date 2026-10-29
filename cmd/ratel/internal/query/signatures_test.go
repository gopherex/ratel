package query_test

import (
	"testing"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testSignatures(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()

	for _, tc := range []inferenceCase{
		{
			name: "literal overload", sql: `SELECT abs(1)`,
			columns: "int4", nullable: "0",
		},
		{
			name: "implicit cast", sql: `SELECT public.widen(1)`,
			columns: "int8", nullable: "1",
		},
		{
			name: "polymorphic", sql: `SELECT array_append(ARRAY[1,2],3)`,
			columns: "_int4", nullable: "1",
		},
		{
			name: "unknown string", sql: `SELECT length('hello')`,
			columns: "int4", nullable: "0",
		},
		{
			name: "variadic", sql: `SELECT concat('id=',1)`,
			columns: "text", nullable: "1",
		},
		{
			name: "json variadic", sql: `SELECT jsonb_build_object('a',1,'b',true)`,
			columns: "jsonb", nullable: "1",
		},
		{name: "enum cast", sql: `SELECT $1::public.status`, params: "status", columns: "status", nullable: "0"},
		{
			name: "domain overload", sql: `SELECT abs(value),public.widen(value) FROM public.money`,
			columns: "int4,int8", nullable: "01",
		},
		{
			name: "domain common", sql: `SELECT coalesce(value,NULL),coalesce(value,value) FROM public.money`,
			columns: "int4,amount", nullable: "00",
		},
		{name: "user defaults", sql: `SELECT public.with_default(1)`, columns: "int4", nullable: "1"},
		{
			name: "user variadic", sql: `SELECT public.collect(1,2),public.collect(VARIADIC ARRAY[1,2])`,
			columns: "int4,int4", nullable: "11",
		},
		{name: "user overloads", sql: `SELECT public.tricky(1,2),public.tricky(1,'x')`, columns: "int4,text", nullable: "11"},
		{name: "quoted identities", sql: `SELECT "x.y".pick(1),x."y.pick"(1)`, columns: "int4,text", nullable: "11"},
		{
			name: "table function", sql: `SELECT id FROM public.identifiers($1)`,
			params: "int8", columns: "int8", nullable: "1",
		},
		{
			name: "default args", sql: `SELECT make_interval(1)`,
			columns: "interval", nullable: "1",
		},
	} {
		runInference(t, parser, cat, signatures, tc)
	}
}

func benchmarkSignatures(b *testing.B) {
	b.Helper()
	benchmarkSQL(b, `SELECT abs($1),public.widen(score),$2=$3 FROM public.users`)
}
