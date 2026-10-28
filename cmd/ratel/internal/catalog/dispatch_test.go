package catalog_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestDeclarations(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct{ name, sql string }{
		{"types", `CREATE TYPE status AS ENUM ('ok'); CREATE DOMAIN positive AS int CHECK(VALUE>0);
   CREATE TYPE pair AS (x int,y text); CREATE TYPE span AS RANGE(subtype=int4);`},
		{"sequence", `CREATE SEQUENCE ids; CREATE TABLE a(id int DEFAULT nextval('ids'));`},
		{"routine", `CREATE FUNCTION f(v int) RETURNS int LANGUAGE sql AS 'SELECT v';
   CREATE PROCEDURE p() LANGUAGE sql AS 'SELECT 1'; CREATE TABLE a(id int DEFAULT f(1));`},
		{"trigger", `CREATE FUNCTION changed() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
   CREATE TABLE a(id int);
   CREATE TRIGGER changed BEFORE INSERT ON a FOR EACH ROW EXECUTE FUNCTION changed();`},
		{"views", `CREATE TABLE a(id int); CREATE VIEW v AS SELECT id FROM a;
   CREATE MATERIALIZED VIEW mv AS SELECT id FROM v;`},
		{"extension", `CREATE EXTENSION pgcrypto; COMMENT ON EXTENSION pgcrypto IS 'crypto';`},
		{"partition", `CREATE TABLE a(id int) PARTITION BY RANGE(id);
   CREATE TABLE a1 PARTITION OF a FOR VALUES FROM(0) TO(10);`},
		{"type comment", `CREATE TYPE status AS ENUM ('ok'); COMMENT ON TYPE status IS 'state';`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build([]catalog.File{{Name: "declarations.sql", SQL: tc.sql}}, 17, nil)
			if err != nil {
				t.Fatal(err)
			}

			if len(result.GetSchemas()) == 0 {
				t.Fatal("missing schema")
			}
		})
	}
}

func TestAttachments(t *testing.T) {
	parse.Warm()
	t.Parallel()

	sql := `COMMENT ON COLUMN a.id IS 'key'; CREATE POLICY p ON a USING(true);
 CREATE INDEX idx ON a(id); ALTER TABLE a ADD CONSTRAINT positive CHECK(id>0),
 ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY; CREATE TABLE a(id int);
 GRANT SELECT ON a TO PUBLIC;`

	result, err := catalog.Build([]catalog.File{{Name: "attachments.sql", SQL: sql}}, 17, nil)
	if err != nil {
		t.Fatal(err)
	}

	schema := result.GetSchemas()[0]
	table := schema.GetTables()[0]

	if len(table.GetConstraints()) != 1 || len(table.GetIndexes()) != 1 ||
		len(table.GetPolicies()) != 1 || len(schema.GetGrants()) != 1 {
		t.Fatalf("missing attachments: %v", table)
	}

	if !table.GetRlsEnabled() || !table.GetRlsForced() {
		t.Fatal("missing RLS flags")
	}

	if table.GetColumns()[0].GetMeta().GetComment() != "key" {
		t.Fatal("column comment missing")
	}
}

func BenchmarkDeclarations(b *testing.B) {
	parse.Warm()

	files := []catalog.File{{Name: "types.sql", SQL: `CREATE TYPE mood AS ENUM ('ok');
 CREATE DOMAIN positive AS int; CREATE TABLE a(v mood,n positive);`}}
	for b.Loop() {
		if _, err := catalog.Build(files, 17, nil); err != nil {
			b.Fatal(err)
		}
	}
}
