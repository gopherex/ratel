package authoring_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func newParser(tb testing.TB) parse.Parser {
	tb.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		tb.Fatal(err)
	}

	return parser
}

func TestSchemaStatements(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	files := []catalog.File{
		{Name: "a.sql", SQL: "CREATE TABLE a.x (id int PRIMARY KEY, y int REFERENCES a.y, FOREIGN KEY (id) REFERENCES a.z)"},
		{Name: "b.sql", SQL: "CREATE VIEW a.v AS SELECT 1; CREATE TABLE a.y (id int PRIMARY KEY)"},
	}

	got, err := authoring.SchemaStatements(parser, files)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"CREATE TABLE a.x (id int PRIMARY KEY, y int)",
		"CREATE VIEW a.v AS SELECT 1",
		" CREATE TABLE a.y (id int PRIMARY KEY)",
		"ALTER TABLE a.x ADD FOREIGN KEY (y) REFERENCES a.y",
		"ALTER TABLE a.x ADD FOREIGN KEY (id) REFERENCES a.z",
	}
	if difference := cmp.Diff(want, got); difference != "" {
		t.Errorf("schemaStatements() (-want +got):\n%s", difference)
	}
}

func BenchmarkSchemaStatements(b *testing.B) {
	parser := newParser(b)
	files := []catalog.File{{Name: "a.sql", SQL: "CREATE TABLE a.x (id int PRIMARY KEY, y int REFERENCES a.y)"}}

	for b.Loop() {
		if _, err := authoring.SchemaStatements(parser, files); err != nil {
			b.Fatal(err)
		}
	}
}
