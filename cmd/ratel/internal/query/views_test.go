package query_test

import (
	"strconv"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/query"
)

func TestViews(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	signatures, err := builtins.For(18)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, sql string
		columns   []string // name:type:nullable
	}{
		{
			name: "view", sql: `CREATE TABLE a(id int PRIMARY KEY, n text);
 CREATE VIEW v AS SELECT id, n FROM a;`,
			columns: []string{"id:int4:false", "n:text:true"},
		},
		{
			name: "view of view", sql: `CREATE TABLE a(id int PRIMARY KEY);
 CREATE VIEW v AS SELECT id FROM a; CREATE VIEW w AS SELECT id + 1 AS next FROM v;`,
			columns: []string{"id:int4:false", "next:int4:false"},
		},
		{
			name: "materialized", sql: `CREATE TABLE a(v numeric NOT NULL);
 CREATE MATERIALIZED VIEW m AS SELECT sum(v) AS total, count(*) AS n FROM a;`,
			columns: []string{"total:numeric:true", "n:int8:false"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cat, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: tc.sql}}, 18, signatures)
			if err != nil {
				t.Fatal(err)
			}

			if err := query.Views(parser, cat, signatures); err != nil {
				t.Fatal(err)
			}

			got := outputs(cat)
			if len(got) != len(tc.columns) {
				t.Fatalf("columns = %v, want %v", got, tc.columns)
			}

			for index := range got {
				if got[index] != tc.columns[index] {
					t.Fatalf("columns = %v, want %v", got, tc.columns)
				}
			}
		})
	}
}

// outputs lists every view output column as name:type:nullable.
func outputs(cat *ir.Catalog) []string {
	var columns []*ir.QueryColumn

	for _, schema := range cat.GetSchemas() {
		for _, view := range schema.GetViews() {
			columns = append(columns, view.GetOutput()...)
		}

		for _, view := range schema.GetMaterializedViews() {
			columns = append(columns, view.GetOutput()...)
		}
	}

	described := make([]string, 0, len(columns))
	for _, column := range columns {
		described = append(described, column.GetName()+":"+column.GetType().GetPgName()+":"+
			strconv.FormatBool(column.GetNullable()))
	}

	return described
}

func BenchmarkViews(b *testing.B) {
	parser, err := parse.New(parse.Config{})
	if err != nil {
		b.Fatal(err)
	}

	signatures, err := builtins.For(18)
	if err != nil {
		b.Fatal(err)
	}

	sql := `CREATE TABLE a(id int PRIMARY KEY, n text); CREATE VIEW v AS SELECT id, n FROM a;`

	for b.Loop() {
		cat, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: sql}}, 18, signatures)
		if err != nil {
			b.Fatal(err)
		}

		if err := query.Views(parser, cat, signatures); err != nil {
			b.Fatal(err)
		}
	}
}
