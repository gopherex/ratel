package catalog_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestNormalization(t *testing.T) {
	parse.Warm()
	t.Parallel()

	files := []catalog.File{{Name: "sorted.sql", SQL: `CREATE TABLE z(b int,a int,
 CONSTRAINT z_check CHECK(b>0),CONSTRAINT a_check CHECK(a>0)); CREATE TABLE a(id int);`}}

	result, err := catalog.Build(files, 17, nil)
	if err != nil {
		t.Fatal(err)
	}

	tables := result.GetSchemas()[0].GetTables()
	if tables[0].GetName().GetParts()[1] != "a" {
		t.Fatal("tables not sorted")
	}

	if tables[1].GetColumns()[0].GetName() != "b" {
		t.Fatal("column order changed")
	}

	if tables[1].GetConstraints()[0].GetName() != "a_check" {
		t.Fatal("constraints not sorted")
	}

	if tables[1].GetConstraints()[0].GetCheck().GetExpression().GetNodeId() == "" {
		t.Fatal("node identity missing")
	}
}

func TestUnnamedIndexes(t *testing.T) {
	parse.Warm()
	t.Parallel()

	files := []catalog.File{{Name: "indexes.sql", SQL: `CREATE INDEX ON a(id);
 CREATE INDEX ON a(value); CREATE TABLE a(id int,value int);`}}

	result, err := catalog.Build(files, 17, nil)
	if err != nil {
		t.Fatal(err)
	}

	indexes := result.GetSchemas()[0].GetTables()[0].GetIndexes()
	if len(indexes) != 2 || indexes[0].GetName() != "a_id_idx" || indexes[1].GetName() != "a_value_idx" {
		t.Fatalf("wrong unnamed index identities: %v", indexes)
	}
}

func BenchmarkNormalization(b *testing.B) {
	parse.Warm()

	files := []catalog.File{{Name: "sorted.sql", SQL: `CREATE TABLE z(id int); CREATE TABLE a(id int);`}}
	for b.Loop() {
		if _, err := catalog.Build(files, 17, nil); err != nil {
			b.Fatal(err)
		}
	}
}
