package annotate_test

import (
	"errors"
	"testing"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testAttach(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		name     string
		owned    []string
		warnings int
	}{
		{name: "unowned", warnings: 1},
		{name: "owned", owned: []string{"go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file := annotate.File{Name: "schema.sql", SQL: "-- @go.row User\nCREATE TABLE a(id int);"}
			meta := &ir.Metadata{Source: &ir.SourceSpan{File: file.Name, EndOffset: uint64(len(file.SQL))}}
			reference := &ir.ObjectRef{
				Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: &ir.QualifiedName{Parts: []string{"public", "a"}},
			}
			targets := []annotate.Target{{Object: reference, Meta: meta}}

			warnings, err := annotate.Attach(parser, file, targets, tc.owned)
			if err != nil {
				t.Fatal(err)
			}

			if len(warnings) != tc.warnings {
				t.Fatalf("warnings = %v", warnings)
			}

			annotation := meta.GetAnnotations()[0]
			if annotation.GetObject().GetName().GetParts()[1] != "a" {
				t.Fatalf("target = %v", annotation)
			}

			if len(warnings) > 0 && warnings[0].GetCode() != "RTL-A02" {
				t.Fatalf("warning = %v", warnings[0])
			}
		})
	}
}

func testDiagnostic(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		sql          string
		line, column uint32
	}{
		{sql: "/* Я */ -- @ratel.bad\nCREATE TABLE a(id int);", line: 1, column: 12},
		{sql: "-- prose\n  -- @ratel.bad\nCREATE TABLE a(id int);", line: 2, column: 6},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			meta := &ir.Metadata{Source: &ir.SourceSpan{File: "schema.sql", EndOffset: uint64(len(tc.sql))}}
			targets := []annotate.Target{{Object: &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE}, Meta: meta}}
			_, err := annotate.Attach(parser, annotate.File{Name: "schema.sql", SQL: tc.sql}, targets, nil)

			var diagnostic *annotate.Error

			if !errors.As(err, &diagnostic) {
				t.Fatalf("error = %v", err)
			}

			source := diagnostic.Diagnostic.GetSource()
			if source.GetStartLine() != tc.line || source.GetStartColumn() != tc.column {
				t.Fatalf("span = %v", source)
			}

			if diagnostic.Diagnostic.GetCode() != "RTL-A01" {
				t.Fatalf("diagnostic = %v", diagnostic)
			}
		})
	}
}

func BenchmarkAttach(b *testing.B) {
	parser := newParser(b)

	file := annotate.File{Name: "schema.sql", SQL: "-- @ratel.origin A\nCREATE TABLE a(id int);"}
	for b.Loop() {
		meta := &ir.Metadata{Source: &ir.SourceSpan{File: file.Name, EndOffset: uint64(len(file.SQL))}}

		targets := []annotate.Target{{Object: &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE}, Meta: meta}}
		if _, err := annotate.Attach(parser, file, targets, nil); err != nil {
			b.Fatal(err)
		}
	}
}
