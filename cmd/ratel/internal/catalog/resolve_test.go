package catalog_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/ir"

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

func testOverloads(t *testing.T, parser parse.Parser) {
	t.Helper()

	signatures, err := builtins.For(18)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, sql, target, failure string }{
		{
			name: "column type picks the overload", sql: `CREATE TABLE a(email text CHECK (length(email) > 3));`,
			target: "pg_catalog.length(text)",
		},
		{
			name: "implicit cast", sql: `CREATE TABLE a(note varchar(10)); CREATE INDEX ON a (lower(note));`,
			target: "pg_catalog.lower(text)",
		},
		{
			name: "domain over base", sql: `CREATE DOMAIN code AS text; CREATE TABLE a(c code CHECK (length(c) > 0));`,
			target: "pg_catalog.length(text)",
		},
		{name: "builtin type outside pgx", sql: `CREATE SEQUENCE s;
 CREATE TABLE a(id bigint DEFAULT nextval('s'::regclass));`},
		{
			name: "untyped arguments stay ambiguous", sql: `CREATE TABLE a(id int CHECK (length(NULL) > 0));`,
			failure: "ambiguous function length",
		},
		{name: "query body defers to inference", sql: `CREATE TABLE a(v numeric);
 CREATE VIEW s AS SELECT sum(v) AS total FROM a;`},
		{name: "default argument", sql: `CREATE FUNCTION f(a int, b int DEFAULT 1) RETURNS int LANGUAGE sql AS 'SELECT a';
 CREATE TABLE a(id int DEFAULT f(1));`, target: "public.f(int4,int4)"},
		{name: "variadic", sql: `CREATE FUNCTION f(VARIADIC v int[]) RETURNS int LANGUAGE sql AS 'SELECT 1';
 CREATE TABLE a(id int DEFAULT f(1, 2, 3));`, target: "public.f(int4[])"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build(parser, []catalog.File{{Name: "overload.sql", SQL: tc.sql}}, 18, signatures)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("error=%v, want %q", err, tc.failure)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if tc.target != "" && !containsTarget(result, tc.target) {
				t.Fatalf("no call bound to %s:\n%s", tc.target, prototext.Format(result))
			}
		})
	}
}

// containsTarget reports whether some call in the catalog binds to the
// routine with the given object id.
func containsTarget(result *ir.Catalog, target string) bool {
	found := false

	var walk func(protoreflect.Message)
	walk = func(message protoreflect.Message) {
		switch call := message.Interface().(type) {
		case *ir.FunctionCall:
			found = found || (call.GetTarget() != nil && catalog.ObjectID(call.GetTarget()) == target)
		default:
		}

		message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			switch {
			case field.Message() == nil || field.IsMap():
			case field.IsList():
				for index := range value.List().Len() {
					walk(value.List().Get(index).Message())
				}
			default:
				walk(value.Message())
			}

			return true
		})
	}

	walk(result.ProtoReflect())

	return found
}

func BenchmarkOverloads(b *testing.B) {
	parser := newParser(b)

	signatures, err := builtins.For(18)
	if err != nil {
		b.Fatal(err)
	}

	files := []catalog.File{{Name: "overload.sql", SQL: `CREATE TABLE a(email text CHECK (length(email) > 3));`}}
	for b.Loop() {
		if _, err := catalog.Build(parser, files, 18, signatures); err != nil {
			b.Fatal(err)
		}
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
