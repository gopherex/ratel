package safety_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func newParser(tb testing.TB) parse.Parser {
	tb.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		tb.Fatal(err)
	}

	return parser
}

func op(kind safety.Kind, relation, other string) safety.Operation {
	return safety.Operation{Kind: kind, Relation: relation, Other: other}
}

func classifyCases() []struct {
	sql  string
	want []safety.Operation
} {
	return []struct {
		sql  string
		want []safety.Operation
	}{
		{"SET lock_timeout = '1s'", nil},
		{"BEGIN", nil},
		{"SELECT 1", nil},
		{"CREATE SCHEMA app", []safety.Operation{op(safety.CreateSchema, "", "")}},
		{"CREATE EXTENSION citext", []safety.Operation{op(safety.CreateExtension, "", "")}},
		{"ALTER EXTENSION citext UPDATE TO '1.6'", []safety.Operation{op(safety.AlterExtension, "", "")}},
		{"CREATE TYPE app.e AS ENUM ('a')", []safety.Operation{op(safety.CreateType, "", "")}},
		{"CREATE TYPE app.c AS (a int)", []safety.Operation{op(safety.CreateType, "", "")}},
		{"CREATE DOMAIN app.d AS int", []safety.Operation{op(safety.CreateType, "", "")}},
		{"CREATE TYPE app.r AS RANGE (subtype = int4)", []safety.Operation{op(safety.CreateType, "", "")}},
		{"ALTER TYPE app.e ADD VALUE 'b'", []safety.Operation{op(safety.AddEnumValue, "", "")}},
		{"ALTER TYPE app.e RENAME VALUE 'a' TO 'b'", []safety.Operation{op(safety.RenameEnumValue, "", "")}},
		{"ALTER DOMAIN app.d SET NOT NULL", []safety.Operation{op(safety.DomainNotNull, "", "")}},
		{"ALTER DOMAIN app.d ADD CONSTRAINT c CHECK (VALUE > 0)", []safety.Operation{op(safety.DomainCheck, "", "")}},
		{
			"ALTER DOMAIN app.d ADD CONSTRAINT c CHECK (VALUE > 0) NOT VALID",
			[]safety.Operation{op(safety.DomainCheckNotValid, "", "")},
		},
		{"ALTER DOMAIN app.d VALIDATE CONSTRAINT c", []safety.Operation{op(safety.DomainValidate, "", "")}},
		{"ALTER DOMAIN app.d SET DEFAULT 1", []safety.Operation{op(safety.AlterDomain, "", "")}},
		{"CREATE SEQUENCE app.s", []safety.Operation{op(safety.CreateSequence, "app.s", "")}},
		{"ALTER SEQUENCE app.s CYCLE", []safety.Operation{op(safety.AlterSequence, "app.s", "")}},
		{"CREATE FUNCTION app.f() RETURNS int AS 'SELECT 1' LANGUAGE sql", []safety.Operation{
			op(safety.CreateRoutine, "", ""),
		}},
		{"CREATE OR REPLACE FUNCTION app.f() RETURNS int AS 'SELECT 1' LANGUAGE sql", []safety.Operation{
			op(safety.ReplaceRoutine, "", ""),
		}},
		{"CREATE TABLE t (id int, u int REFERENCES app.u, FOREIGN KEY (id) REFERENCES app.v)", []safety.Operation{
			op(safety.CreateTable, "public.t", ""), op(safety.CreateTable, "public.t", "app.u"),
			op(safety.CreateTable, "public.t", "app.v"),
		}},
		{"CREATE TABLE app.p1 PARTITION OF app.p DEFAULT", []safety.Operation{
			op(safety.CreateTable, "app.p1", ""), op(safety.CreatePartition, "app.p", "app.p1"),
		}},
		{`CREATE TABLE app."User" (id int)`, []safety.Operation{op(safety.CreateTable, `app."User"`, "")}},
		{`CREATE TABLE app."order" (id int)`, []safety.Operation{op(safety.CreateTable, `app."order"`, "")}},
		{`CREATE TABLE app.name (id int)`, []safety.Operation{op(safety.CreateTable, "app.name", "")}},
		{"CREATE INDEX i ON app.t (x)", []safety.Operation{op(safety.CreateIndex, "app.t", "")}},
		{"CREATE INDEX CONCURRENTLY i ON app.t (x)", []safety.Operation{op(safety.CreateIndexConcurrently, "app.t", "")}},
		{"CREATE INDEX i ON ONLY app.p (x)", []safety.Operation{op(safety.CreateIndexOnly, "app.p", "")}},
		{"CREATE VIEW app.v AS SELECT 1", []safety.Operation{op(safety.CreateView, "app.v", "")}},
		{"CREATE OR REPLACE VIEW app.v AS SELECT 1", []safety.Operation{op(safety.ReplaceView, "app.v", "")}},
		{"CREATE MATERIALIZED VIEW app.m AS SELECT 1", []safety.Operation{op(safety.CreateView, "app.m", "")}},
		{"REFRESH MATERIALIZED VIEW app.m", []safety.Operation{op(safety.RefreshView, "app.m", "")}},
		{"REINDEX INDEX app.i", []safety.Operation{op(safety.Reindex, "app.t", "")}},
		{"REINDEX (CONCURRENTLY) TABLE app.t", []safety.Operation{op(safety.ReindexConcurrently, "app.t", "")}},
		{"CREATE TRIGGER x AFTER INSERT ON app.t EXECUTE FUNCTION app.f()", []safety.Operation{
			op(safety.CreateTrigger, "app.t", ""),
		}},
		{"CREATE POLICY p ON app.t USING (true)", []safety.Operation{op(safety.CreatePolicy, "app.t", "")}},
		{"ALTER POLICY p ON app.t USING (false)", []safety.Operation{op(safety.CreatePolicy, "app.t", "")}},
		{"COMMENT ON TABLE app.t IS 'x'", []safety.Operation{op(safety.Comment, "app.t", "")}},
		{"COMMENT ON COLUMN app.t.c IS 'x'", []safety.Operation{op(safety.Comment, "app.t", "")}},
		{"COMMENT ON SCHEMA app IS 'x'", []safety.Operation{op(safety.Comment, "", "")}},
		{"GRANT SELECT ON app.t, app.u TO PUBLIC", []safety.Operation{
			op(safety.Grant, "app.t", ""), op(safety.Grant, "app.u", ""),
		}},
		{"TRUNCATE app.t", []safety.Operation{op(safety.Truncate, "app.t", "")}},
		{"CLUSTER app.t USING i", []safety.Operation{op(safety.RewriteTable, "app.t", "")}},
		{"INSERT INTO app.t VALUES (1)", []safety.Operation{op(safety.DataChange, "app.t", "")}},
		{"UPDATE app.t SET x = 1", []safety.Operation{op(safety.DataChange, "app.t", "")}},
		{"DELETE FROM app.t", []safety.Operation{op(safety.DataChange, "app.t", "")}},
		{"MERGE INTO app.t USING app.u ON true WHEN MATCHED THEN DELETE", []safety.Operation{
			op(safety.DataChange, "app.t", ""),
		}},
		{"DO $$BEGIN END$$", []safety.Operation{op(safety.Unknown, "", "")}},
	}
}

func alterCases() []struct {
	sql  string
	want []safety.Operation
} {
	return []struct {
		sql  string
		want []safety.Operation
	}{
		{"ALTER TABLE app.t ADD COLUMN c int, DROP COLUMN d", []safety.Operation{
			op(safety.AddColumn, "app.t", ""), op(safety.DropColumn, "app.t", ""),
		}},
		{"ALTER TABLE app.t ADD COLUMN c int NOT NULL", []safety.Operation{
			{Kind: safety.AddColumn, Relation: "app.t", Codes: []string{"RTL-C03"}},
		}},
		{"ALTER TABLE app.t ADD COLUMN c int NOT NULL DEFAULT 0", []safety.Operation{op(safety.AddColumn, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c TYPE bigint", []safety.Operation{op(safety.AlterColumnType, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c SET DEFAULT 1", []safety.Operation{op(safety.SetDefault, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c DROP DEFAULT", []safety.Operation{op(safety.DropDefault, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c SET NOT NULL", []safety.Operation{op(safety.SetNotNull, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c DROP NOT NULL", []safety.Operation{op(safety.DropNotNull, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c DROP IDENTITY", []safety.Operation{op(safety.AlterIdentity, "app.t", "")}},
		{"ALTER TABLE app.t ALTER COLUMN c SET EXPRESSION AS (1)", []safety.Operation{
			op(safety.SetExpression, "app.t", ""),
		}},
		{"ALTER TABLE app.t ALTER COLUMN c DROP EXPRESSION", []safety.Operation{op(safety.DropExpression, "app.t", "")}},
		{"ALTER TABLE app.t ADD CONSTRAINT c CHECK (x > 0)", []safety.Operation{op(safety.AddCheck, "app.t", "")}},
		{"ALTER TABLE app.t ADD CONSTRAINT c CHECK (x > 0) NOT VALID", []safety.Operation{
			op(safety.AddCheckNotValid, "app.t", ""),
		}},
		{"ALTER TABLE app.t ADD FOREIGN KEY (x) REFERENCES app.u", []safety.Operation{
			op(safety.AddForeignKey, "app.t", "app.u"),
		}},
		{"ALTER TABLE app.t ADD FOREIGN KEY (x) REFERENCES app.u NOT VALID", []safety.Operation{
			op(safety.AddForeignKeyNotValid, "app.t", "app.u"),
		}},
		{"ALTER TABLE app.t ADD PRIMARY KEY (x)", []safety.Operation{op(safety.AddUnique, "app.t", "")}},
		{"ALTER TABLE app.t ADD CONSTRAINT u UNIQUE USING INDEX i", []safety.Operation{
			op(safety.AddConstraintIndex, "app.t", ""),
		}},
		{"ALTER TABLE app.t ADD EXCLUDE USING gist (r WITH &&)", []safety.Operation{op(safety.AddExclusion, "app.t", "")}},
		{"ALTER TABLE app.t VALIDATE CONSTRAINT c", []safety.Operation{op(safety.ValidateConstraint, "app.t", "")}},
		{"ALTER TABLE app.t DROP CONSTRAINT c", []safety.Operation{op(safety.DropConstraint, "app.t", "")}},
		{"ALTER TABLE app.t SET UNLOGGED", []safety.Operation{op(safety.SetLogged, "app.t", "")}},
		{"ALTER TABLE app.t SET TABLESPACE x", []safety.Operation{op(safety.RewriteTable, "app.t", "")}},
		{"ALTER TABLE app.t ENABLE ROW LEVEL SECURITY", []safety.Operation{op(safety.AlterRLS, "app.t", "")}},
		{"ALTER TABLE app.t CLUSTER ON i", []safety.Operation{op(safety.Unknown, "app.t", "")}},
		{"ALTER TABLE app.p ATTACH PARTITION app.c DEFAULT", []safety.Operation{
			op(safety.AttachPartition, "app.p", "app.c"),
		}},
		{"ALTER INDEX app.pi ATTACH PARTITION app.ci", []safety.Operation{op(safety.AttachIndex, "app.pi", "")}},
		{"ALTER TABLE app.p DETACH PARTITION app.c", []safety.Operation{op(safety.DetachPartition, "app.p", "app.c")}},
		{"ALTER TABLE app.p DETACH PARTITION app.c CONCURRENTLY", []safety.Operation{
			op(safety.DetachConcurrently, "app.p", "app.c"),
		}},
		{"ALTER TABLE app.p DETACH PARTITION app.c FINALIZE", []safety.Operation{
			op(safety.DetachPartitionFinalize, "app.p", "app.c"),
		}},
		{"ALTER TABLE app.t RENAME TO u", []safety.Operation{op(safety.RenameTable, "app.t", "")}},
		{"ALTER TABLE app.t RENAME COLUMN a TO b", []safety.Operation{op(safety.RenameColumn, "app.t", "")}},
		{"ALTER TABLE app.t RENAME CONSTRAINT a TO b", []safety.Operation{op(safety.RenameConstraint, "app.t", "")}},
		{"ALTER INDEX app.i RENAME TO j", []safety.Operation{op(safety.RenameIndex, "app.i", "")}},
		{"ALTER SCHEMA app RENAME TO b", []safety.Operation{op(safety.Unknown, "", "")}},
	}
}

func dropCases() []struct {
	sql  string
	want []safety.Operation
} {
	return []struct {
		sql  string
		want []safety.Operation
	}{
		{"DROP SCHEMA app", []safety.Operation{op(safety.DropSchema, "", "")}},
		{"DROP EXTENSION citext", []safety.Operation{op(safety.DropExtension, "", "")}},
		{"DROP TYPE app.e", []safety.Operation{op(safety.DropType, "", "")}},
		{"DROP DOMAIN app.d", []safety.Operation{op(safety.DropType, "", "")}},
		{"DROP SEQUENCE app.s", []safety.Operation{op(safety.DropSequence, "app.s", "")}},
		{"DROP FUNCTION app.f()", []safety.Operation{op(safety.DropRoutine, "", "")}},
		{"DROP TABLE app.t, u", []safety.Operation{op(safety.DropTable, "app.t", ""), op(safety.DropTable, "public.u", "")}},
		{"DROP INDEX app.i", []safety.Operation{op(safety.DropIndex, "app.t", "")}},
		{"DROP INDEX CONCURRENTLY app.i", []safety.Operation{op(safety.DropIndexConcurrently, "app.t", "")}},
		{"DROP VIEW app.v", []safety.Operation{op(safety.DropView, "app.v", "")}},
		{"DROP MATERIALIZED VIEW app.m", []safety.Operation{op(safety.DropView, "app.m", "")}},
		{"DROP TRIGGER x ON app.t", []safety.Operation{op(safety.DropTrigger, "app.t", "")}},
		{"DROP POLICY p ON app.t", []safety.Operation{op(safety.DropPolicy, "app.t", "")}},
		{"DROP CAST (int AS text)", []safety.Operation{op(safety.Unknown, "", "")}},
	}
}

func TestClassifier(t *testing.T) {
	parser := newParser(t)

	t.Parallel()
	checkClassified(t, parser, classifyCases())
}

// checkClassified takes a parser built before the test went parallel:
// wazero's first runtime must not race another.
func checkClassified(t *testing.T, parser parse.Parser, cases []struct {
	sql  string
	want []safety.Operation
},
) {
	t.Helper()

	env := safety.Env{Indexes: map[string]string{"app.i": "app.t"}}

	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			statements, err := parser.Statements(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			got := safety.NewClassifier(parser, env).Operations(statements[0].Node)
			if difference := cmp.Diff(tc.want, got); difference != "" {
				t.Errorf("Operations(%q) (-want +got):\n%s", tc.sql, difference)
			}
		})
	}
}

func BenchmarkClassifier(b *testing.B) {
	parser := newParser(b)

	statements, err := parser.Statements(`ALTER TABLE app."Order" ADD COLUMN c int NOT NULL,
		ADD CONSTRAINT f FOREIGN KEY (c) REFERENCES app.u NOT VALID`)
	if err != nil {
		b.Fatal(err)
	}

	classifier := safety.NewClassifier(parser, safety.Env{})

	for b.Loop() {
		classifier.Operations(statements[0].Node)
	}
}
