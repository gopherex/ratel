package catalog_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testResolution(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct{ name, sql, failure string }{
		{"target column", `CREATE TABLE a(id int PRIMARY KEY);
   CREATE TABLE b(id int REFERENCES a(missing));`, "undeclared column"},
		{"source column", `CREATE TABLE a(id int,FOREIGN KEY(missing) REFERENCES a(id));`, "undeclared column"},
		{"key count", `CREATE TABLE a(id int PRIMARY KEY);
   CREATE TABLE b(x int,y int,FOREIGN KEY(x,y) REFERENCES a);`, "column count"},
		{"index target", `CREATE INDEX idx ON absent(id);`, "undeclared table"},
		{"index column", `CREATE TABLE a(id int); CREATE INDEX idx ON a(missing);`, "undeclared column"},
		{"check column", `CREATE TABLE a(id int CHECK(missing>0));`, "undeclared column"},
		{"quoted sequence", `CREATE SCHEMA "a.b"; CREATE SEQUENCE "a.b"."s.q";
 CREATE TABLE a(id int DEFAULT nextval('"a.b"."s.q"'));`, ""},
		{"canonical signature", `CREATE TYPE status AS ENUM ('ok');
 CREATE FUNCTION f(v status) RETURNS int LANGUAGE sql AS 'SELECT 1';
 COMMENT ON FUNCTION f(status) IS 'function';`, ""},
		{"duplicate canonical signature", `CREATE TYPE status AS ENUM ('ok');
 CREATE FUNCTION f(v status) RETURNS int LANGUAGE sql AS 'SELECT 1';
 CREATE FUNCTION f(v public.status) RETURNS int LANGUAGE sql AS 'SELECT 1';`, "duplicate"},
		{"row type namespace", `CREATE TABLE a(id int); CREATE TYPE a AS ENUM ('ok');`, "duplicate"},
		{"comment target", `COMMENT ON TABLE absent IS 'x';`, "undeclared"},
		{"grant target", `GRANT SELECT ON absent TO PUBLIC;`, "undeclared"},
		{"view target", `CREATE VIEW v AS SELECT id FROM absent;`, "undeclared"},
		{"duplicate policy", `CREATE TABLE a(id int); CREATE POLICY p ON a USING(true);
   CREATE POLICY p ON a USING(true);`, "duplicate"},
		{"relation namespace", `CREATE TABLE a(id int); CREATE SEQUENCE a;`, "duplicate"},
		{"type namespace", `CREATE TYPE a AS ENUM ('x'); CREATE DOMAIN a AS int;`, "duplicate"},
		{"wrong function arity", `CREATE FUNCTION f(v int) RETURNS int LANGUAGE sql AS 'SELECT v';
   CREATE TABLE a(id int DEFAULT f());`, "undeclared"},
		{"function overload", `CREATE FUNCTION f(v int) RETURNS int LANGUAGE sql AS 'SELECT v';
   CREATE FUNCTION f(v text) RETURNS int LANGUAGE sql AS 'SELECT 1';
   CREATE TABLE a(id int DEFAULT f(1));`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := catalog.Build(parser, []catalog.File{{Name: "resolve.sql", SQL: tc.sql}}, 17, nil)
			if tc.failure == "" {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("error=%v, want %q", err, tc.failure)
			}
		})
	}
}

func testBuiltinSnapshot(t *testing.T, parser parse.Parser) {
	t.Helper()

	signatures, err := builtins.For(17)
	if err != nil {
		t.Fatal(err)
	}

	files := []catalog.File{{Name: "builtin.sql", SQL: `CREATE TABLE a(id int DEFAULT abs((-1)::int4));`}}

	result, err := catalog.Build(parser, files, 17, signatures)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.GetSchemas()) != 1 {
		t.Fatal("builtins leaked into desired catalog")
	}
}

func BenchmarkResolution(b *testing.B) {
	parser := newParser(b)

	files := []catalog.File{{Name: "resolve.sql", SQL: `CREATE TABLE a(id int PRIMARY KEY);
 CREATE TABLE b(id int REFERENCES a);`}}
	for b.Loop() {
		if _, err := catalog.Build(parser, files, 17, nil); err != nil {
			b.Fatal(err)
		}
	}
}
