package lint_test

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/lint"
)

func TestSchema(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	snapshot, err := builtins.For(18)
	if err != nil {
		t.Fatal(err)
	}

	sql := `CREATE SCHEMA app;
CREATE TABLE app.u (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY);
CREATE TABLE app.t (
  id serial PRIMARY KEY,
  u bigint REFERENCES app.u,
  w bigint REFERENCES app.u,
  at timestamp,
  code char(2)
);
CREATE INDEX t_w ON app.t (w, id);
-- @ratel.allow RTL-P01 reason=small
CREATE TABLE app.s (u bigint REFERENCES app.u, k bigint PRIMARY KEY);`

	built, err := catalog.Build(parser, []catalog.File{{Name: "s.sql", SQL: sql}}, 18, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	findings := lint.Schema(built)
	got := make([]string, 0, len(findings))

	for _, finding := range findings {
		mark := ""
		if finding.Allowed {
			mark = " allowed"
		}

		got = append(got, finding.Code+" "+finding.Where+mark)
	}

	slices.Sort(got)

	want := []string{
		"RTL-P01 constraint app.s.s_u_fkey allowed",
		"RTL-P01 constraint app.t.t_u_fkey",
		"RTL-P02 column app.t.at",
		"RTL-P02 column app.t.code",
		"RTL-P02 column app.t.id",
	}
	if difference := cmp.Diff(want, got); difference != "" {
		t.Errorf("Schema() (-want +got):\n%s", difference)
	}
}

func BenchmarkSchema(b *testing.B) {
	parser := newParser(b)

	built, err := catalog.Build(parser, []catalog.File{{Name: "s.sql", SQL: `CREATE TABLE u (id int8 PRIMARY KEY);
CREATE TABLE t (id serial PRIMARY KEY, u int8 REFERENCES u);`}}, 18, nil)
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		lint.Schema(built)
	}
}
