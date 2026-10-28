package mapper_test

import (
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testMapType(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		sql, name  string
		kind       ir.TypeKind
		dimensions uint32
	}{
		{"integer", "int4", ir.TypeKind_TYPE_KIND_SCALAR, 0},
		{"numeric(12,2)", "numeric", ir.TypeKind_TYPE_KIND_SCALAR, 0},
		{"varchar(20)[][]", "_varchar", ir.TypeKind_TYPE_KIND_ARRAY, 2},
		{"app.status", "status", ir.TypeKind_TYPE_KIND_USER_DEFINED, 0},
		{"timestamp(3) with time zone", "timestamptz", ir.TypeKind_TYPE_KIND_SCALAR, 0},
		{"interval day to second(3)", "interval", ir.TypeKind_TYPE_KIND_SCALAR, 0},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parser.Parse("CREATE TABLE t (v " + tc.sql + ")")
			if err != nil {
				t.Fatal(err)
			}

			typ := tree.GetStmts()[0].GetStmt().GetCreateStmt().GetTableElts()[0].GetColumnDef().GetTypeName()

			got := mapper.MapType(typ)
			if got.GetPgName() != tc.name || got.GetKind() != tc.kind || got.GetArrayDimensions() != tc.dimensions {
				t.Fatalf("type = %v", got)
			}

			if tc.sql == "numeric(12,2)" && (got.GetModifier().GetNumeric().GetPrecision() != 12 ||
				got.GetModifier().GetNumeric().GetScale() != 2) {
				t.Fatal(got)
			}

			if tc.sql == "interval day to second(3)" && got.GetModifier().GetInterval().GetFields() != "DAY TO SECOND" {
				t.Fatal(got)
			}
		})
	}
}

func BenchmarkMapType(b *testing.B) {
	parser := newParser(b)

	tree, err := parser.Parse("CREATE TABLE t (v numeric(12,2)[])")
	if err != nil {
		b.Fatal(err)
	}

	typ := tree.GetStmts()[0].GetStmt().GetCreateStmt().GetTableElts()[0].GetColumnDef().GetTypeName()
	for b.Loop() {
		mapper.MapType(typ)
	}
}
