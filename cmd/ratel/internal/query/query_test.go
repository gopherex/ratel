package query_test

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/query"
)

const schemaSQL = `CREATE TABLE public.users(
 id bigint PRIMARY KEY, name text NOT NULL, manager_id bigint, score integer NOT NULL);
CREATE TABLE public.arrays(id int4 PRIMARY KEY,items int4[] NOT NULL);
CREATE DOMAIN public.amount AS int4;
CREATE TABLE public.money(value public.amount NOT NULL);
CREATE VIEW public.people AS SELECT id,name FROM public.users;
CREATE VIEW public.unqualified_people AS SELECT id FROM users;
CREATE SCHEMA "x.y";
CREATE SCHEMA x;
CREATE FUNCTION "x.y".pick(value int4) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION x."y.pick"(value int4) RETURNS text LANGUAGE sql AS 'SELECT value::text';
CREATE FUNCTION public.tricky(value int4,extra int4 DEFAULT 0) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.tricky(value int4,extra text DEFAULT '') RETURNS text LANGUAGE sql AS 'SELECT extra';
CREATE FUNCTION public.with_default(value int4,extra int4 DEFAULT 0) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.collect(VARIADIC items int4[]) RETURNS int4 LANGUAGE sql AS 'SELECT 1';
CREATE FUNCTION public.user_rows() RETURNS SETOF public.users LANGUAGE sql AS 'SELECT * FROM public.users';
CREATE FUNCTION public.choose_kind(value bool) RETURNS bool LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.choose_kind(value int4) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE TYPE public.status AS ENUM ('active');
CREATE FUNCTION public.identifiers(value bigint) RETURNS TABLE(id bigint)
 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.widen(value bigint) RETURNS bigint LANGUAGE sql STRICT AS 'SELECT value';`

type inferenceCase struct{ name, sql, params, columns, nullable string }

func TestInfer(t *testing.T) {
	t.Parallel()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	signatures, err := builtins.For(17)
	if err != nil {
		t.Fatal(err)
	}

	cat, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: schemaSQL}}, 17, signatures)
	if err != nil {
		t.Fatal(err)
	}

	cases := []inferenceCase{
		{"unqualified", `SELECT id FROM users WHERE id=@id`, "int8", "int8", "0"},
		{
			"column", `SELECT id, name, manager_id FROM public.users WHERE id = @id`,
			"int8", "int8,text,int8", "001",
		},
		{
			"join", `SELECT u.id, m.id FROM public.users u LEFT JOIN public.users m ON m.id = u.manager_id`,
			"", "int8,int8", "01",
		},
		{
			"coalesce", `SELECT coalesce(manager_id, 0), CASE WHEN id > 0 THEN name ELSE NULL END FROM public.users`,
			"", "int8,text", "01",
		},
		{
			"aggregate", `SELECT count(*), sum(score), row_number() OVER (ORDER BY id) FROM public.users`,
			"", "int8,int8,int8", "010",
		},
		{
			"overload", `SELECT abs(score), public.widen(score), score + 1 FROM public.users`,
			"", "int4,int8,int4", "010",
		},
		{
			"any", `SELECT id FROM public.users WHERE id = ANY(@ids) LIMIT @limit OFFSET @offset`,
			"_int8,int8,int8", "int8", "0",
		},
		{
			"nested", `SELECT (SELECT name FROM public.users WHERE id = $1) AS name`,
			"int8", "text", "1",
		},
		{
			"cte", `WITH x AS (SELECT id FROM public.users WHERE score = $1) SELECT id FROM x`,
			"int4", "int8", "0",
		},
		{
			"recursive", `WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM x WHERE n < $1) SELECT n FROM x`,
			"int4", "int4", "0",
		},
		{
			"union", `SELECT 1 AS n UNION ALL SELECT 2::bigint`,
			"", "int8", "0",
		},
		{
			"values", `VALUES (1, 'x'), (2, NULL)`,
			"", "int4,text", "01",
		},
		{
			"insert", `INSERT INTO public.users(id,name,score) VALUES($1,$2,$3) RETURNING id,name`,
			"int8,text,int4", "int8,text", "00",
		},
		{
			"upsert", `INSERT INTO public.users(id,name,score) VALUES($1,$2,$3) 
 ON CONFLICT(id) DO UPDATE SET name=$4 WHERE users.score > $5 RETURNING id`,
			"int8,text,int4,text,int4", "int8", "0",
		},
		{
			"update", `UPDATE public.users SET name=$1 WHERE id=$2 RETURNING name`,
			"text,int8", "text", "0",
		},
		{
			"delete", `DELETE FROM public.users WHERE id=$1 RETURNING id`,
			"int8", "int8", "0",
		},
		{
			"dmlcte", `WITH x AS (DELETE FROM public.users WHERE id=$1 RETURNING name) SELECT name FROM x`,
			"int8", "text", "0",
		},
		{
			"unnest", `SELECT n, ord FROM unnest($1::int[]) WITH ORDINALITY AS t(n,ord)`,
			"_int4", "int4,int8", "10",
		},
		{
			"json", `SELECT '{}'::jsonb ->> 'missing' AS value`,
			"", "text", "1",
		},
		{
			"lateral", `SELECT u.id, x.name FROM public.users u 
 LEFT JOIN LATERAL (SELECT name FROM public.users WHERE id=u.manager_id) x ON true`,
			"", "int8,text", "01",
		},
		{
			"unconstrained target", `SELECT $1 AS value`,
			"text", "text", "0",
		},
		{"nullable annotation", "-- @ratel.nullable id\nSELECT @id::int8 AS result", "int8", "int8", "1"},
		{"notnull annotation", "-- @ratel.notnull result\nSELECT manager_id AS result FROM public.users", "", "int8", "0"},
	}
	for _, tc := range cases {
		runInference(t, parser, cat, signatures, tc)
	}

	testExpressions(t, parser, cat, signatures)
	testScopes(t, parser, cat, signatures)
	testStatements(t, parser, cat, signatures)
	testSignatures(t, parser, cat, signatures)
	testSpecial(t, parser, cat, signatures)
	testProvenance(t, parser, cat, signatures)
	t.Run("errors", func(t *testing.T) {
		t.Parallel()

		for _, sql := range []string{
			`SELECT missing(id) FROM public.users`, `SELECT $1 IS NULL`, `SELECT nope FROM public.users`,
			`SELECT $1 AS id FROM public.users WHERE id=$1`,
			`SELECT $1 IS NULL FROM public.users WHERE id=$1`,
			`INSERT INTO public.users(id,name,score) SELECT $1,$2,$3 FROM public.users u WHERE u.score=$1`,
			`SELECT id FROM public.users WHERE id=$2`, `SELECT $1::int4, lower($1)`,
			`SELECT @name::text,$1::int4`, `SELECT abs($1::uuid)`, `SELECT $1::nosuchtype`,
			`SELECT range_agg('[1,2)'::int4range)`, `SELECT public.tricky(1)`,
			`SELECT public.choose_kind($1)`, `SELECT ARRAY[ROW(1,'x')]`,
		} {
			t.Run(sql, func(t *testing.T) {
				t.Parallel()

				_, err := query.Infer(parser, cat, signatures, []*annotate.QuerySource{{
					Name: "Bad", SQL: sql, Meta: &ir.Metadata{Source: &ir.SourceSpan{
						File: "bad.sql", StartLine: 4, StartColumn: 7,
					}},
				}})

				var inferenceError *query.Error

				if !errors.As(err, &inferenceError) || inferenceError.Diagnostic.GetCode() != "RTL-Q01" ||
					inferenceError.Diagnostic.GetSeverity() != ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR ||
					!strings.Contains(err.Error(), "Bad") || !strings.HasPrefix(err.Error(), "bad.sql:4:7: RTL-Q01:") {
					t.Fatalf("expected RTL-Q01 diagnostic, got %v", err)
				}
			})
		}
	})
}

func testProvenance(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()
	t.Run("provenance and ownership", func(t *testing.T) {
		t.Parallel()

		sourceSQL := `-- name: Users :many
-- @ratel.row UserResult
-- @go.row example.com/models.User
SELECT u.id AS other_id,u.name,u.id+1 AS id FROM public.users u WHERE u.id=@id;
-- name: Write :one
INSERT INTO public.users(id,name,manager_id,score) VALUES(@id,@name,@manager,@score) RETURNING manager_id;`

		sources, err := annotate.Queries(parser, []annotate.File{{Name: "users.sql", SQL: sourceSQL}}, []string{"go"})
		if err != nil {
			t.Fatal(err)
		}

		before := proto.CloneOf(cat)
		meta := proto.CloneOf(sources.Queries[0].Meta)

		first, err := query.Infer(parser, cat, signatures, sources.Queries)
		if err != nil {
			t.Fatal(err)
		}

		second, err := query.Infer(parser, cat, signatures, sources.Queries)
		if err != nil {
			t.Fatal(err)
		}

		result := first[0]
		assertWrite(t, first)

		if !proto.Equal(result, second[0]) || !proto.Equal(cat, before) || !proto.Equal(sources.Queries[0].Meta, meta) {
			t.Fatal("inference changed inputs or produced nondeterministic output")
		}

		if !proto.Equal(result.GetAst().GetMeta().GetSource(), sources.Queries[0].Meta.GetSource()) {
			t.Fatal("AST lost original query source position")
		}

		columns := result.GetColumns()
		if columns[0].GetSourceColumn().GetName().GetParts()[2] != "id" || columns[0].GetTableAlias() != "u" ||
			columns[2].GetSourceColumn() != nil {
			t.Fatalf("wrong column provenance: %v", columns)
		}

		if len(result.GetMeta().GetAnnotations()) != 2 || result.GetParameters()[0].GetName() != "id" ||
			result.GetParameters()[0].GetColumn() == nil {
			t.Fatalf("lost metadata or parameter provenance: %v", result)
		}
	})
}

func assertWrite(t *testing.T, queries []*ir.Query) {
	t.Helper()

	param := queries[1].GetParameters()[2]
	if !param.GetNullable() || param.GetColumn() == nil ||
		queries[1].GetAst().GetNodeId() == queries[0].GetAst().GetNodeId() {
		t.Fatalf("write parameter provenance or AST identity missing: %v", queries[1])
	}
}

func runInference(t *testing.T, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema, tc inferenceCase) {
	t.Helper()
	t.Run(tc.name, func(t *testing.T) {
		t.Parallel()

		files := []annotate.File{{Name: "query.sql", SQL: "-- name: Test :many\n" + tc.sql}}

		sources, err := annotate.Queries(parser, files, nil)
		if err != nil {
			t.Fatal(err)
		}

		got, err := query.Infer(parser, cat, signatures, sources.Queries)
		if err != nil {
			t.Fatal(err)
		}

		params := make([]string, 0, len(got[0].GetParameters()))
		for _, param := range got[0].GetParameters() {
			params = append(params, param.GetType().GetPgName())
		}

		if tc.name == "enum cast" && got[0].GetColumns()[0].GetType().GetKind() != ir.TypeKind_TYPE_KIND_ENUM {
			t.Fatal("cast did not resolve enum identity")
		}

		if tc.name == "unqualified" && !strings.Contains(got[0].GetSql(), "public.users") {
			t.Fatal("physical table reference is not schema-qualified")
		}

		if tc.name == "scalar alias" && got[0].GetColumns()[0].GetName() != "x" {
			t.Fatal("scalar function column alias lost")
		}

		shape := columnSignature(got[0])
		if strings.Join(params, ",") != tc.params || shape.columns != tc.columns || shape.nullable != tc.nullable {
			t.Fatalf("params=%v columns=%s nullable=%s; want %s / %s / %s", params, shape.columns, shape.nullable,
				tc.params, tc.columns, tc.nullable)
		}
	})
}

type columnShape struct{ columns, nullable string }

func columnSignature(item *ir.Query) columnShape {
	columns := make([]string, 0, len(item.GetColumns()))

	var nullable strings.Builder

	for _, col := range item.GetColumns() {
		columns = append(columns, col.GetType().GetPgName())

		if col.GetNullable() {
			nullable.WriteByte('1')
		} else {
			nullable.WriteByte('0')
		}
	}

	return columnShape{columns: strings.Join(columns, ","), nullable: nullable.String()}
}

func BenchmarkInfer(b *testing.B) {
	for _, tc := range []struct {
		name string
		run  func(*testing.B)
	}{
		{"query", func(b *testing.B) {
			b.Helper()
			benchmarkSQL(b, `SELECT id,name FROM public.users WHERE id=ANY($1) LIMIT $2`)
		}},
		{"expressions", benchmarkExpressions},
		{"scopes", benchmarkScopes},
		{"statements", benchmarkStatements},
		{"signatures", benchmarkSignatures},
		{"special", benchmarkSpecial},
	} {
		b.Run(tc.name, tc.run)
	}
}

func benchmarkSQL(b *testing.B, sql string) {
	b.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		b.Fatal(err)
	}

	signatures, err := builtins.For(17)
	if err != nil {
		b.Fatal(err)
	}

	cat, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: schemaSQL}}, 17, signatures)
	if err != nil {
		b.Fatal(err)
	}

	sources := []*annotate.QuerySource{{Name: "Users", SQL: sql}}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := query.Infer(parser, cat, signatures, sources); err != nil {
			b.Fatal(err)
		}
	}
}
