package mapper_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestMapExpr(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct{ sql, field, symbol string }{
		{"12345678901234567890.123456789", "literal", ""},
		{"NULL", "literal", ""},
		{"true", "literal", ""},
		{"'text'", "literal", ""},
		{"42", "literal", ""},
		{"$1", "parameter", ""},
		{"app.t.id", "column_ref", ""},
		{"t.*", "star", ""},
		{"a NOT LIKE 'x'", "operator", "NOT LIKE"},
		{"a NOT ILIKE 'x'", "operator", "NOT ILIKE"},
		{"a IS NOT TRUE", "operator", "IS NOT TRUE"},
		{"a IS NOT NULL", "operator", "IS NOT NULL"},
		{"a IN (1,2)", "operator", "IN"},
		{"a BETWEEN 1 AND 2", "operator", "BETWEEN"},
		{"a AND NOT b", "operator", "AND"},
		{"1::bigint", "cast", ""},
		{"CASE a WHEN 1 THEN 2 ELSE 3 END", "case_expr", ""},
		{"ROW(1,2)", "list", ""},
		{"ARRAY[1,2]", "raw_sql", ""},
		{"B'101'", "raw_sql", ""},
		{`"a.b".c`, "column_ref", ""},
		{"least(a,b)", "function_call", ""},
		{"a NOT IN (1,2)", "operator", "NOT IN"},
		{"a IS DISTINCT FROM b", "operator", "IS DISTINCT FROM"},
		{"a IS NOT DISTINCT FROM b", "operator", "IS NOT DISTINCT FROM"},
		{"a NOT BETWEEN SYMMETRIC 1 AND 2", "operator", "NOT BETWEEN SYMMETRIC"},
		{"a BETWEEN SYMMETRIC 1 AND 2", "operator", "BETWEEN SYMMETRIC"},
		{"a NOT BETWEEN 1 AND 2", "operator", "NOT BETWEEN"},
		{"a LIKE 'x'", "operator", "LIKE"},
		{"a + ANY(ARRAY[1,2])", "operator", "+ ANY"},
		{"a = ALL(ARRAY[1,2])", "operator", "= ALL"},
		{"nullif(a,b)", "operator", "NULLIF"},
		{"(SELECT 1)", "subquery", ""},
		{"a IN (SELECT b FROM t)", "subquery", ""},
		{"a = ALL(SELECT b FROM t)", "subquery", ""},
		{"ARRAY(SELECT b FROM t)", "raw_sql", ""},
		{"EXISTS (SELECT 1)", "subquery", ""},
		{"a = ANY(SELECT b FROM t)", "subquery", ""},
		{"coalesce(a,1)", "function_call", ""},
		{"greatest(a,1)", "function_call", ""},
		{
			"sum(a) FILTER (WHERE a > 0) OVER (ORDER BY a ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)",
			"function_call", "",
		},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			sql := "SELECT " + tc.sql

			tree, err := parse.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("expr.sql", sql, raw, 0)
			expr := current.MapExpr(raw.GetStmt().GetSelectStmt().GetTargetList()[0].GetResTarget().GetVal())
			oneof := expr.ProtoReflect().Descriptor().Oneofs().Get(0)

			if string(expr.ProtoReflect().WhichOneof(oneof).Name()) != tc.field {
				t.Fatal(expr)
			}

			if tc.symbol != "" && expr.GetOperator().GetSymbol() != tc.symbol {
				t.Fatal(expr)
			}

			if tc.sql == "12345678901234567890.123456789" &&
				expr.GetLiteral().GetNumericValue() != tc.sql {
				t.Fatal("decimal lost precision", expr)
			}

			if tc.field == "raw_sql" && expr.GetRawSql() == "" {
				t.Fatal(expr)
			}

			if tc.sql == `"a.b".c` && expr.GetColumnRef().GetQualifier() != `"a.b"` {
				t.Fatal(expr)
			}

			if expr.GetNodeId() == "" {
				t.Fatal("missing node id")
			}
		})
	}
}

func BenchmarkMapExpr(b *testing.B) {
	sql := "SELECT coalesce(a, 1) + $1"

	tree, err := parse.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}

	raw := tree.GetStmts()[0]
	current := mapper.New("expr.sql", sql, raw, 0)
	node := raw.GetStmt().GetSelectStmt().GetTargetList()[0].GetResTarget().GetVal()

	for b.Loop() {
		current.MapExpr(node)
	}
}
