package annotate_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testValidate(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct{ name, annotation, failure string }{
		{"rename", "@ratel.renamed from=old", ""},
		{"missing key", "@ratel.renamed", "missing"},
		{"extra value", "@ratel.renamed from=old extra", "positional"},
		{"empty value", "@ratel.renamed from=\"\"", "empty"},
		{"backfill", "@ratel.backfill expr=42 batch=100", ""},
		{"SQL expression", "@ratel.retype using=\"coalesce(old, 0)::int\"", ""},
		{"invalid expression", "@ratel.retype using=\"1 +\"", "expression"},
		{"multiple statements", "@ratel.backfill expr=\"1);SELECT(2\"", "expression"},
		{"query instead of expression", "@ratel.retype using=\"1) ORDER BY (1\"", "expression"},
		{"bad code", "@ratel.allow RTL-bad reason=reviewed", "code"},
		{"bad relation name", "@ratel.rel name=lower", "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sql := "-- " + tc.annotation + "\nCREATE TABLE a(id int);"
			meta := &ir.Metadata{Source: &ir.SourceSpan{File: "schema.sql", EndOffset: uint64(len(sql))}}
			target := annotate.Target{
				Object: &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_COLUMN}, Meta: meta, ForeignKey: true,
			}

			_, err := annotate.Attach(parser, annotate.File{Name: "schema.sql", SQL: sql}, []annotate.Target{target}, nil)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("error = %v, want %q", err, tc.failure)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(meta.GetAnnotations()) != 1 {
				t.Fatalf("annotations = %v", meta.GetAnnotations())
			}
		})
	}
}

func BenchmarkValidate(b *testing.B) {
	parser := newParser(b)

	sql := "-- @ratel.backfill expr=42 batch=100\nCREATE TABLE a(id int);"
	for b.Loop() {
		meta := &ir.Metadata{Source: &ir.SourceSpan{File: "schema.sql", EndOffset: uint64(len(sql))}}

		target := annotate.Target{Object: &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_COLUMN}, Meta: meta}
		file := annotate.File{Name: "schema.sql", SQL: sql}

		if _, err := annotate.Attach(parser, file, []annotate.Target{target}, nil); err != nil {
			b.Fatal(err)
		}
	}
}
