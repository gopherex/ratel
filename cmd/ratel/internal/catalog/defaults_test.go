package catalog_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestSequenceDefaults(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct {
		name, sql, typ                     string
		start, increment, minimum, maximum int64
	}{
		{"serial", `CREATE TABLE a(id serial PRIMARY KEY);`, "int4", 1, 1, 1, 2147483647},
		{"identity", `CREATE TABLE a(id bigint GENERATED ALWAYS AS IDENTITY);`, "int8", 1, 1, 1, 9223372036854775807},
		{"explicit zero", `CREATE SEQUENCE ids AS smallint MINVALUE 0 START 0;`, "int2", 0, 1, 0, 32767},
		{"descending", `CREATE SEQUENCE ids AS smallint INCREMENT -2;`, "int2", -1, -2, -32768, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build([]catalog.File{{Name: "defaults.sql", SQL: tc.sql}}, 17, nil)
			if err != nil {
				t.Fatal(err)
			}

			sequences := result.GetSchemas()[0].GetSequences()
			if len(sequences) != 1 {
				t.Fatalf("sequences=%v", sequences)
			}

			sequence := sequences[0]
			if sequence.GetDataType().GetPgName() != tc.typ || sequence.GetStart() != tc.start ||
				sequence.GetIncrement() != tc.increment || sequence.GetMinValue() != tc.minimum ||
				sequence.GetMaxValue() != tc.maximum || sequence.GetCache() != 1 {
				t.Fatalf("incorrect sequence defaults: %v", sequence)
			}
		})
	}
}

func TestGeneratedMetadata(t *testing.T) {
	parse.Warm()
	t.Parallel()

	files := []catalog.File{{Name: "metadata.sql", SQL: `CREATE TABLE a(id serial PRIMARY KEY);
 COMMENT ON COLUMN a.id IS 'identity';`}}

	result, err := catalog.Build(files, 17, nil)
	if err != nil {
		t.Fatal(err)
	}

	schema := result.GetSchemas()[0]
	if schema.GetSequences()[0].GetMeta().GetComment() != "" {
		t.Fatal("column comment leaked into sequence")
	}

	for _, constraint := range schema.GetTables()[0].GetConstraints() {
		if constraint.GetMeta().GetComment() != "" {
			t.Fatal("column comment leaked into constraint")
		}
	}
}

func TestAlterPrimaryNotNull(t *testing.T) {
	parse.Warm()
	t.Parallel()

	files := []catalog.File{{Name: "primary.sql", SQL: `ALTER TABLE a ADD CONSTRAINT pk PRIMARY KEY(id);
 CREATE TABLE a(id int);`}}

	result, err := catalog.Build(files, 17, nil)
	if err != nil {
		t.Fatal(err)
	}

	table := result.GetSchemas()[0].GetTables()[0]
	if table.GetColumns()[0].GetNullable() || len(table.GetConstraints()) != 2 {
		t.Fatalf("primary key lacks normalized not-null constraint: %v", table)
	}
}

func BenchmarkSequenceDefaults(b *testing.B) {
	parse.Warm()

	files := []catalog.File{{Name: "defaults.sql", SQL: `CREATE TABLE a(id serial PRIMARY KEY);`}}
	for b.Loop() {
		if _, err := catalog.Build(files, 17, nil); err != nil {
			b.Fatal(err)
		}
	}
}
