package catalog_test

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestBuild(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct{ name, sql, failure string }{
		{"empty", "", ""},
		{"public schema", `COMMENT ON SCHEMA public IS 'default'; GRANT USAGE ON SCHEMA public TO PUBLIC;`, ""},
		{"extension schema", `CREATE EXTENSION pgcrypto WITH SCHEMA absent;`, "undeclared"},
		{"forward cycle", `CREATE TABLE a(id int PRIMARY KEY, b int REFERENCES b);
 CREATE TABLE b(id int PRIMARY KEY, a int REFERENCES a);`, ""},
		{"view CTE", `CREATE TABLE a(id int); CREATE VIEW v AS
 WITH ids AS (SELECT id FROM a) SELECT id FROM ids;`, ""},
		{"forward type", `CREATE TABLE a(v status); CREATE TYPE status AS ENUM ('ok');`, ""},
		{"embedded", `CREATE SCHEMA app CREATE TABLE a(id int) CREATE VIEW v AS SELECT id FROM app.a;`, ""},
		{"additive", `ALTER TABLE a ADD CONSTRAINT positive CHECK(id>0), ENABLE ROW LEVEL SECURITY;
 CREATE INDEX a_id ON a(id); CREATE POLICY p ON a USING(true);
 COMMENT ON COLUMN a.id IS 'key'; GRANT SELECT ON a TO PUBLIC; CREATE TABLE a(id int);`, ""},
		{"serial", `CREATE TABLE a(id serial PRIMARY KEY);`, ""},
		{"identity", `CREATE TABLE a(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY);`, ""},
		{"unnamed checks", `CREATE TABLE a(id int CHECK(id>0), CHECK(id<100));`, ""},
		{"default public type", `CREATE SCHEMA app; CREATE TYPE status AS ENUM ('ok');
 CREATE TABLE app.a(value status);`, ""},
		{"duplicate comments", `CREATE TABLE a(id int); COMMENT ON TABLE a IS 'one';
 COMMENT ON TABLE a IS 'two';`, "duplicate"},
		{"missing partition", `CREATE TABLE a PARTITION OF absent FOR VALUES FROM(0) TO(10);`, "undeclared"},
		{"unknown table", `CREATE TABLE a(id int REFERENCES absent);`, "undeclared"},
		{"unknown type", `CREATE TABLE a(v absent);`, "undeclared"},
		{"unknown function", `CREATE TABLE a(v int DEFAULT absent());`, "undeclared"},
		{"unknown sequence", `CREATE TABLE a(v int DEFAULT nextval('absent'));`, "undeclared"},
		{"duplicate", "CREATE TABLE a(id int);\nCREATE TABLE a(id int);", "schema.sql:1:1"},
		{"duplicate column", `CREATE TABLE a(id int, id text);`, "duplicate"},
		{"duplicate constraint", `CREATE TABLE a(id int CONSTRAINT same UNIQUE, CONSTRAINT same CHECK(id>0));`, "duplicate"},
		{"missing schema", `CREATE TABLE absent.a(id int);`, "undeclared"},
		{"drop", `DROP TABLE a;`, "not allowed"},
		{"insert", `INSERT INTO a VALUES(1);`, "not allowed"},
		{"alter column", `ALTER TABLE a ADD COLUMN id int;`, "not allowed"},
		{"concurrent", `CREATE INDEX CONCURRENTLY idx ON a(id);`, "not allowed"},
		{"revoke", `REVOKE SELECT ON a FROM PUBLIC;`, "not allowed"},
		{"column grant", `GRANT SELECT(id) ON a TO PUBLIC;`, "not allowed"},
		{"table as", `CREATE TABLE a AS SELECT 1;`, "not allowed"},
		{"syntax", `CREATE TABLE`, "parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build([]catalog.File{{Name: "schema.sql", SQL: tc.sql}}, 17, nil)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("error = %v, want %q", err, tc.failure)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if result == nil {
				t.Fatal("nil catalog")
			}
		})
	}
}

func TestPermutations(t *testing.T) {
	parse.Warm()
	t.Parallel()

	statements := []string{
		`CREATE SCHEMA app`,
		`CREATE TABLE app.a(id int PRIMARY KEY, b int REFERENCES app.b, status app.status)`,
		`CREATE TABLE app.b(id int PRIMARY KEY, a int REFERENCES app.a)`,
		`CREATE TYPE app.status AS ENUM ('ok','bad')`,
		`ALTER TABLE app.a ADD CONSTRAINT positive CHECK(id>0)`,
		`CREATE INDEX a_b ON app.a(b)`,
		`CREATE INDEX ON app.a(b)`,
		`CREATE INDEX ON app.a(b DESC)`,
		`ALTER TABLE app.a ADD CHECK(id<100)`,
		`ALTER TABLE app.a ADD CHECK(id<200)`,
		`COMMENT ON TABLE app.a IS 'A'`,
		`CREATE VIEW app.v AS SELECT id FROM app.a`,
	}
	files := make([]catalog.File, len(statements))

	for index, sql := range statements {
		files[index] = catalog.File{Name: strings.Repeat("f", index+1) + ".sql", SQL: sql + ";"}
	}

	expected := buildBytes(t, files, false)

	random := rand.New(rand.NewPCG(1, 2))
	for range 50 {
		random.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })

		if !bytes.Equal(expected, buildBytes(t, files, false)) {
			t.Fatal("file order changes catalog")
		}
	}

	expected = buildBytes(t, []catalog.File{{Name: "one.sql", SQL: strings.Join(statements, "; ") + ";"}}, true)

	for range 50 {
		random.Shuffle(len(statements), func(i, j int) { statements[i], statements[j] = statements[j], statements[i] })

		permuted := []catalog.File{{Name: "one.sql", SQL: strings.Join(statements, "; ") + ";"}}
		if !bytes.Equal(expected, buildBytes(t, permuted, true)) {
			t.Fatal("statement order changes catalog")
		}
	}
}

func buildBytes(t *testing.T, files []catalog.File, clearSpans bool) []byte {
	t.Helper()

	result, err := catalog.Build(files, 17, nil)
	if err != nil {
		t.Fatal(err)
	}

	if clearSpans {
		clearSources(result.ProtoReflect())
	}

	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func clearSources(message protoreflect.Message) {
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Message() == nil {
			return true
		}

		if field.Message().FullName() == "ratel.v1.ir.SourceSpan" {
			message.Clear(field)

			return true
		}

		if field.IsList() {
			list := value.List()
			for index := range list.Len() {
				clearSources(list.Get(index).Message())
			}
		} else {
			clearSources(value.Message())
		}

		return true
	})
}

func TestVersion(t *testing.T) {
	t.Parallel()

	for _, major := range []int{0, 15, 19} {
		t.Run(string(rune('a'+major)), func(t *testing.T) {
			t.Parallel()

			if _, err := catalog.Build(nil, major, nil); err == nil {
				t.Fatal("accepted unsupported version")
			}
		})
	}
}

func BenchmarkBuild(b *testing.B) {
	parse.Warm()

	files := []catalog.File{{Name: "schema.sql", SQL: `CREATE TABLE a(id int PRIMARY KEY);
 CREATE TABLE b(id int REFERENCES a);`}}
	for b.Loop() {
		if _, err := catalog.Build(files, 17, nil); err != nil {
			b.Fatal(err)
		}
	}
}
