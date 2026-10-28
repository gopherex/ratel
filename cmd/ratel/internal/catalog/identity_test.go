package catalog_test

import (
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
)

func TestObjectID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		kind  ir.ObjectKind
		parts []string
		want  string
	}{
		{"simple", ir.ObjectKind_OBJECT_KIND_TABLE, []string{"app", "users"}, "app.users"},
		{"quoted", ir.ObjectKind_OBJECT_KIND_TABLE, []string{"A.B", `a"b`}, `"A.B"."a""b"`},
		{"function", ir.ObjectKind_OBJECT_KIND_FUNCTION, []string{"app", "f", "int4,text"}, "app.f(int4,text)"},
		{"empty signature", ir.ObjectKind_OBJECT_KIND_FUNCTION, []string{"public", "f", ""}, "public.f()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := catalog.ObjectID(&ir.ObjectRef{Kind: tc.kind, Name: &ir.QualifiedName{Parts: tc.parts}})
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func BenchmarkObjectID(b *testing.B) {
	reference := &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_FUNCTION, Name: &ir.QualifiedName{Parts: []string{"app", "f", "int4,text"}},
	}
	for b.Loop() {
		_ = catalog.ObjectID(reference)
	}
}
