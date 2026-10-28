package catalog_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestOwnedNamespaces(t *testing.T) {
	parser := newParser(t)
	t.Parallel()

	for _, tc := range []struct {
		name     string
		owned    []string
		warnings int
	}{
		{"unowned", nil, 1}, {"owned", []string{"fixture"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			files := []catalog.File{{Name: "schema.sql", SQL: "-- @fixture.model User\nCREATE TABLE users(id int);"}}

			result, err := catalog.Build(parser, files, 18, nil, tc.owned...)
			if err != nil {
				t.Fatal(err)
			}

			if len(result.GetDiagnostics()) != tc.warnings {
				t.Fatal(result.GetDiagnostics())
			}
		})
	}
}

func testAnnotations(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct{ name, sql, failure string }{
		{"objects", "-- @ratel.origin proto.A\nCREATE TABLE a(\n-- @ratel.origin proto.A.id\n" +
			"id int PRIMARY KEY,\nbody text -- @go.type example.com/pkg.Text\n);", ""},
		{"foreign key", "CREATE TABLE a(id int PRIMARY KEY); CREATE TABLE b(\n" +
			"-- @ratel.rel name=Author inverse=Posts\na int REFERENCES a);", ""},
		{"embedded", "CREATE SCHEMA app\n-- @ratel.origin A\nCREATE TABLE a(\n" +
			"-- @ratel.origin A.id\nid int);", ""},
		{"embedded grant", "CREATE SCHEMA app CREATE TABLE a(id int)\n" +
			"-- @ratel.allow RTL-L01 reason=reviewed\n" +
			"GRANT CREATE, USAGE ON SCHEMA app TO editor WITH GRANT OPTION;", ""},
		{"inline block", "CREATE TABLE a(/* @ratel.origin A.id */ id int);", ""},
		{"table constraint", "CREATE TABLE a(id int PRIMARY KEY);CREATE TABLE b(id int,\n" +
			"-- @ratel.rel name=Parent\nCONSTRAINT fk FOREIGN KEY(id) REFERENCES a);", ""},
		{"alter constraint", "CREATE TABLE a(id int PRIMARY KEY); CREATE TABLE b(id int);\n" +
			"-- @ratel.rel name=Parent\nALTER TABLE b ADD CONSTRAINT fk FOREIGN KEY(id) REFERENCES a;", ""},
		{"index", "CREATE TABLE a(id int);\n-- @ratel.renamed from=old\nCREATE INDEX idx ON a(id);", ""},
		{"type", "-- @ratel.renamed from=old\nCREATE TYPE state AS ENUM ('ok');CREATE TABLE a(id int);", ""},
		{"retype", "CREATE TABLE a(\n-- @ratel.retype using=old::int\nid int);", ""},
		{"allow", "-- @ratel.allow RTL-L01 reason=reviewed\nCREATE TABLE a(id int);", ""},
		{"comment target", "CREATE TABLE a(id int);\n-- @ratel.origin A\nCOMMENT ON TABLE a IS 'hello';", ""},
		{"grant target", "CREATE TABLE a(id int);\n-- @ratel.allow RTL-L01 reason=reviewed\n" +
			"GRANT SELECT ON a TO PUBLIC;", ""},
		{"ambiguous grant", "CREATE TABLE a(id int);CREATE TABLE b(id int);\n" +
			"-- @ratel.allow RTL-L01 reason=reviewed\nGRANT SELECT ON a,b TO PUBLIC;", "target"},
		{"unknown", "-- @ratel.unknown\nCREATE TABLE a(id int);", "unknown"},
		{"wrong scope", "-- @ratel.row Result\nCREATE TABLE a(id int);", "target"},
		{"not foreign key", "CREATE TABLE a(\n-- @ratel.rel name=Author\nid int);", "foreign key"},
		{"unknown key", "-- @ratel.renamed from=old extra=no\nCREATE TABLE a(id int);", "key"},
		{"duplicate", "-- @ratel.origin A\n-- @ratel.origin B\nCREATE TABLE a(id int);", "duplicate"},
		{"conflict", "CREATE TABLE a(id int PRIMARY KEY);CREATE TABLE b(\n" +
			"-- @ratel.rel name=A\n-- @ratel.norel\nid int REFERENCES a);", "conflict"},
		{"orphan", "CREATE TABLE a(id int);\n-- @ratel.origin A", "target"},
		{"outside column list", "CREATE TABLE a(id int) /* @ratel.origin A */;", "target"},
		{"trailing comma", "CREATE TABLE a(id int, -- @ratel.origin A.id\nbody text);", ""},
		{"composite", "CREATE TYPE pair AS (\n-- @go.type example.com/ID\nid int);CREATE TABLE a(id int);", ""},
		{"invalid batch", "CREATE TABLE a(\n-- @ratel.backfill expr=1 batch=0\nid int);", "batch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: tc.sql}}, 17, nil)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) || !strings.Contains(err.Error(), "schema.sql:") {
					t.Fatalf("error = %v, want %q", err, tc.failure)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			table := result.GetSchemas()[0].GetTables()[0]
			if tc.name == "objects" {
				if len(table.GetMeta().GetAnnotations()) != 1 ||
					len(table.GetColumns()[0].GetMeta().GetAnnotations()) != 1 ||
					len(table.GetColumns()[1].GetMeta().GetAnnotations()) != 1 {
					t.Fatalf("annotations missing: %v", table)
				}

				annotation := table.GetColumns()[0].GetMeta().GetAnnotations()[0]
				if annotation.GetObject().GetName().GetParts()[2] != "id" || annotation.GetSource().GetStartLine() != 3 {
					t.Fatalf("wrong target/span: %v", annotation)
				}
			}
		})
	}
}

func testAnnotationTargets(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		name, sql, object string
		selectMeta        func(*ir.Catalog) *ir.Metadata
	}{
		{
			"composite", "CREATE TYPE pair AS (\n-- @go.type example.com/ID\nid int);", "public.pair.id",
			func(result *ir.Catalog) *ir.Metadata {
				return result.GetSchemas()[0].GetComposites()[0].GetFields()[0].GetMeta()
			},
		},
		{
			"domain constraint", "CREATE DOMAIN d AS int\n-- @ratel.renamed from=old\nCONSTRAINT positive CHECK (VALUE>0);",
			"public.d.positive", func(result *ir.Catalog) *ir.Metadata {
				return result.GetSchemas()[0].GetDomains()[0].GetConstraints()[0].GetMeta()
			},
		},
		{
			"embedded", "CREATE SCHEMA app\n-- @ratel.origin A\nCREATE TABLE a(id int);", "app.a",
			func(result *ir.Catalog) *ir.Metadata { return result.GetSchemas()[0].GetTables()[0].GetMeta() },
		},
		{
			"constraint", "CREATE TABLE a(id int PRIMARY KEY);CREATE TABLE b(id int,\n" +
				"-- @ratel.rel name=Parent\nCONSTRAINT fk FOREIGN KEY(id) REFERENCES a);", "public.b.fk",
			func(result *ir.Catalog) *ir.Metadata {
				return result.GetSchemas()[0].GetTables()[1].GetConstraints()[0].GetMeta()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: tc.sql}}, 17, nil)
			if err != nil {
				t.Fatal(err)
			}

			annotations := tc.selectMeta(result).GetAnnotations()
			if len(annotations) != 1 {
				t.Fatalf("annotations = %v", annotations)
			}

			if actual := catalog.ObjectID(annotations[0].GetObject()); actual != tc.object {
				t.Fatalf("target = %s, want %s", actual, tc.object)
			}

			if tc.name == "composite" && len(result.GetDiagnostics()) != 1 {
				t.Fatal("missing plugin warning")
			}
		})
	}
}

func BenchmarkAnnotations(b *testing.B) {
	parser := newParser(b)

	files := []catalog.File{{Name: "schema.sql", SQL: "-- @ratel.origin A\nCREATE TABLE a(id int);"}}
	for b.Loop() {
		if _, err := catalog.Build(parser, files, 17, nil); err != nil {
			b.Fatal(err)
		}
	}
}
