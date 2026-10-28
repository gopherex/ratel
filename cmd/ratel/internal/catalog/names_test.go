package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// Expected names are what PostgreSQL 18 chooses for the same DDL.
func testNames(t *testing.T, parser parse.Parser) {
	t.Helper()

	signatures, err := builtins.For(18)
	if err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("t", 60)

	for _, tc := range []struct{ name, sql, want string }{
		{"check on one column", `CREATE TABLE a(n int CHECK (n > 0));`, "a_n_check"},
		{"check on two columns", `CREATE TABLE a(x int, y int, CHECK (x < y));`, "a_check"},
		{"check without columns", `CREATE TABLE a(x int, CHECK (true));`, "a_check"},
		{"duplicate check", `CREATE TABLE a(x int, CHECK (x > 0), CHECK (x < 9));`, "a_x_check1"},
		{"unique", `CREATE TABLE a(x int, y int, UNIQUE (x, y));`, "a_x_y_key"},
		{"long table keeps label", `CREATE TABLE ` + long + `(id int PRIMARY KEY);`, long[:58] + "_pkey"},
		{
			"long parts shrink the longer", `CREATE TABLE ` + long + `(` + long + ` int UNIQUE);`,
			long[:29] + "_" + long[:29] + "_key",
		},
		{"domain check", `CREATE DOMAIN d AS int CHECK (VALUE > 0);`, "d_check"},
		{"second domain check", `CREATE DOMAIN d AS int CHECK (VALUE > 0) CHECK (VALUE < 9);`, "d_check1"},
		{"index on expression", `CREATE TABLE a(x text); CREATE INDEX ON a (lower(x));`, "a_lower_idx"},
		{"materialized view index", `CREATE TABLE a(x int); CREATE MATERIALIZED VIEW m AS SELECT x FROM a;
 CREATE INDEX ON m (x);`, "m_x_idx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build(parser, []catalog.File{{Name: "names.sql", SQL: tc.sql}}, 18, signatures)
			if err != nil {
				t.Fatal(err)
			}

			if names := objectNames(result); !slices.Contains(names, tc.want) {
				t.Fatalf("names = %v, want %s", names, tc.want)
			}
		})
	}
}

func objectNames(result *ir.Catalog) []string {
	var names []string

	for _, schema := range result.GetSchemas() {
		for _, table := range schema.GetTables() {
			for _, constraint := range table.GetConstraints() {
				names = append(names, constraint.GetName())
			}

			for _, index := range table.GetIndexes() {
				names = append(names, index.GetName())
			}
		}

		for _, view := range schema.GetMaterializedViews() {
			for _, index := range view.GetIndexes() {
				names = append(names, index.GetName())
			}
		}

		for _, domain := range schema.GetDomains() {
			for _, constraint := range domain.GetConstraints() {
				names = append(names, constraint.GetName())
			}
		}
	}

	return names
}

func BenchmarkNames(b *testing.B) {
	parser := newParser(b)

	files := []catalog.File{{Name: "names.sql", SQL: `CREATE TABLE a(x int, y int, CHECK (x < y), UNIQUE (x, y));
 CREATE DOMAIN d AS int CHECK (VALUE > 0);`}}
	for b.Loop() {
		if _, err := catalog.Build(parser, files, 18, nil); err != nil {
			b.Fatal(err)
		}
	}
}
