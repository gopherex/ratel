package mapper_test

import (
	"testing"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testMapRoutine(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		sql   string
		check func(*testing.T, mapper.Mapper, *pg.Node)
	}{
		{"CREATE DOMAIN app.positive AS int DEFAULT 1 NOT NULL CONSTRAINT pos CHECK (VALUE > 0)", checkDomain},
		{"CREATE TYPE app.pair AS (a int, b text COLLATE app.c)", checkComposite},
		{"CREATE TYPE app.timerange AS RANGE (subtype = timestamptz," +
			" subtype_diff = pg_catalog.tstzrange_subdiff)", checkRange},
		{"CREATE FUNCTION app.f(a int DEFAULT 1) RETURNS TABLE(x int, y text) LANGUAGE sql" +
			" IMMUTABLE STRICT PARALLEL SAFE SECURITY DEFINER COST 5 ROWS 10 AS $$ SELECT a, 'x' $$", checkFunction},
		{"CREATE PROCEDURE app.p(a int) LANGUAGE sql AS $$ SELECT a $$", checkProcedure},
		{"CREATE TRIGGER audit BEFORE INSERT OR UPDATE OF id ON app.t FOR EACH ROW" +
			" WHEN (new.id>0) EXECUTE FUNCTION app.audit('')", checkTrigger},
		{"CREATE MATERIALIZED VIEW app.mv(x) TABLESPACE data AS SELECT 1 WITH NO DATA", checkMaterializedView},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New(parser, "routine.sql", tc.sql, raw, 0)
			tc.check(t, current, raw.GetStmt())
		})
	}
}

func checkDomain(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	got := current.MapCreateDomain(node.GetCreateDomainStmt())
	if got.GetNullable() || len(got.GetConstraints()) != 1 || got.GetDefaultExpr() == nil {
		t.Fatal(got)
	}
}

func checkComposite(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	got := current.MapCompositeType(node.GetCompositeTypeStmt())
	if len(got.GetFields()) != 2 || got.GetFields()[1].GetCollation() != "app.c" {
		t.Fatal(got)
	}
}

func checkRange(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	got := current.MapCreateRange(node.GetCreateRangeStmt())
	if got.GetSubtype().GetPgName() != "timestamptz" || got.GetCanonical() != "" ||
		got.GetSubtypeDiff() != "pg_catalog.tstzrange_subdiff" {
		t.Fatal(got)
	}
}

func checkFunction(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	function, procedure := current.MapCreateFunction(node.GetCreateFunctionStmt())
	if procedure != nil || len(function.GetReturns().GetTable().GetColumns()) != 2 ||
		function.GetEstimatedCost() != 5 || function.GetEstimatedRows() != 10 ||
		len(function.GetName().GetParts()) != 3 || function.GetName().GetParts()[2] != "int4" {
		t.Fatal(function)
	}
}

func checkProcedure(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	function, procedure := current.MapCreateFunction(node.GetCreateFunctionStmt())
	if procedure.GetLanguage() != "sql" || function != nil {
		t.Fatal(procedure)
	}
}

func checkTrigger(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	got := current.MapCreateTrigger(node.GetCreateTrigStmt())
	if len(got.GetEvents()) != 2 || got.GetWhen() == nil || len(got.GetArguments()) != 1 ||
		len(got.GetFunction().GetName().GetParts()) != 3 {
		t.Fatal(got)
	}
}

func checkMaterializedView(t *testing.T, current mapper.Mapper, node *pg.Node) {
	t.Helper()

	got := current.MapMaterializedView(node.GetCreateTableAsStmt())
	if got.GetWithData() || got.GetQuery() == nil || got.GetTablespace() != "data" {
		t.Fatal(got)
	}
}

func testFunctionReturns(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		sql, signature, returns string
		bodyCount               int
	}{
		{"CREATE FUNCTION f(a int) RETURNS int LANGUAGE sql RETURN a+1", "int4", "scalar", 1},
		{"CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END", "", "scalar", 1},
		{"CREATE FUNCTION f(a app.custom[], OUT b int) LANGUAGE sql AS 'SELECT 1'", "app.custom[]", "scalar", 0},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql AS 'SELECT 1'", "", "void_value", 0},
		{"CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'", "", "trigger_value", 0},
		{"CREATE FUNCTION f() RETURNS SETOF int LANGUAGE sql AS 'SELECT 1'", "", "setof", 0},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New(parser, "function.sql", tc.sql, raw, 0)

			function, procedure := current.MapCreateFunction(raw.GetStmt().GetCreateFunctionStmt())
			if procedure != nil || function.GetName().GetParts()[1] != tc.signature ||
				len(function.GetSqlBody()) != tc.bodyCount {
				t.Fatal(function)
			}

			reflected := function.GetReturns().ProtoReflect()

			field := reflected.WhichOneof(reflected.Descriptor().Oneofs().Get(0))
			if field == nil || string(field.Name()) != tc.returns {
				t.Fatal(function)
			}
		})
	}
}

func BenchmarkRoutine(b *testing.B) {
	parser := newParser(b)

	sql := "CREATE FUNCTION app.f(a int DEFAULT 1) RETURNS int LANGUAGE sql AS $$ SELECT a $$"

	tree, err := parser.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}

	raw := tree.GetStmts()[0]
	current := mapper.New(parser, "bench.sql", sql, raw, 0)

	for b.Loop() {
		current.MapCreateFunction(raw.GetStmt().GetCreateFunctionStmt())
	}
}
