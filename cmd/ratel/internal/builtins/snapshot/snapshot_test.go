package snapshot_test

import (
	"bytes"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins/snapshot"
)

func TestEncode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		reverse bool
	}{{"forward", false}, {"reverse", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			schema := fixture()

			expected, err := snapshot.Encode(schema)
			if err != nil {
				t.Fatal(err)
			}

			if tc.reverse {
				slices.Reverse(schema.GetFunctions())
				slices.Reverse(schema.GetBuiltinTypes())
				slices.Reverse(schema.GetBuiltinOperators())
				slices.Reverse(schema.GetBuiltinCasts())
			}

			before := proto.Clone(schema)

			actual, err := snapshot.Encode(schema)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(expected, actual) {
				t.Fatal("row order changed snapshot bytes")
			}

			if !proto.Equal(before, schema) {
				t.Fatal("encoding mutated source")
			}
		})
	}
}

func fixture() *ir.Schema {
	integer := &ir.TypeRef{PgName: "int4"}
	text := &ir.TypeRef{PgName: "text"}

	return &ir.Schema{
		Name: "pg_catalog",
		Functions: []*ir.Function{
			{Name: &ir.QualifiedName{Parts: []string{"pg_catalog", "f", "int4"}}},
			{Name: &ir.QualifiedName{Parts: []string{"pg_catalog", "f", "text"}}},
		},
		BuiltinTypes:     []*ir.BuiltinType{{Type: integer}, {Type: text}},
		BuiltinOperators: []*ir.BuiltinOperator{{Name: "+", Left: integer, Right: integer}, {Name: "+", Right: integer}},
		BuiltinCasts:     []*ir.BuiltinCast{{Source: integer, Target: text}, {Source: text, Target: integer}},
	}
}

func BenchmarkEncode(b *testing.B) {
	schema := fixture()
	for b.Loop() {
		if _, err := snapshot.Encode(schema); err != nil {
			b.Fatal(err)
		}
	}
}
