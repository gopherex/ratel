package lint_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/lint"
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

// target holds app.t and app.u.
func target() safety.Env {
	return safety.Env{Major: 18, Relations: map[string]safety.Relation{"app.t": {}, "app.u": {}}}
}

// codes lints a hand-written file and lists "code allowed" per finding.
func codes(t *testing.T, parser parse.Parser, sql string, policies map[string]bool) []string {
	t.Helper()

	file, err := authoring.Parse(migrate.Source{Name: "1_x.sql", SQL: sql})
	if err != nil {
		t.Fatal(err)
	}

	authoring.Classify(&file, safety.NewClassifier(parser, target()), parser)

	findings := lint.Steps(&file, target(), policies)
	result := make([]string, 0, len(findings))

	for _, finding := range findings {
		mark := ""
		if finding.Allowed {
			mark = " allowed"
		}

		result = append(result, finding.Code+mark)
	}

	return result
}

func TestHandWrittenSteps(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	for _, tc := range []struct {
		name, sql string
		policies  map[string]bool
		want      []string
	}{
		{name: "index", sql: "CREATE INDEX i ON app.t (a);", want: []string{"RTL-L02"}},
		{name: "concurrent index", sql: "CREATE INDEX CONCURRENTLY i ON app.t (a);"},
		{
			name: "allowed drop", sql: "-- @ratel.allow RTL-D01 reason=x\n-- @ratel.allow RTL-C02 reason=x\nDROP TABLE app.t;",
			want: []string{"RTL-C02 allowed", "RTL-D01 allowed"},
		},
		{name: "new table", sql: "CREATE TABLE app.n (a int);\nCREATE INDEX i ON app.n (a);"},
		{
			name: "own statement in a block", sql: "-- ratel:step\nCREATE INDEX CONCURRENTLY i ON app.t (a);\nSELECT 1;",
			want: []string{"RTL-T01"},
		},
		{
			name: "enum value used", sql: "-- ratel:step\nALTER TYPE app.e ADD VALUE 'b';\nSELECT 'b'::app.e;",
			want: []string{"RTL-C05", "RTL-T02"},
		},
		{name: "scan after a lock", sql: "-- ratel:step\nALTER TABLE app.t ADD COLUMN c int;\n" +
			"ALTER TABLE app.t VALIDATE CONSTRAINT k;", want: []string{"RTL-T03"}},
		{name: "own timeout", sql: "SET lock_timeout = 0;", want: []string{"RTL-T04"}},
		{
			name: "two tables", sql: "-- ratel:step\nALTER TABLE app.t ADD COLUMN c int;\nALTER TABLE app.u ADD COLUMN c int;",
			want: []string{"RTL-T05"},
		},
		{name: "data change", sql: "UPDATE app.t SET a = 1;", want: []string{"RTL-T06"}},
		{name: "backfill", sql: "-- ratel:step phase=backfill\nUPDATE app.t SET a = 1;"},
		{
			name: "long with a lock", sql: "-- ratel:step class=long\nALTER TABLE app.t ADD COLUMN c int;",
			want: []string{"RTL-L01"},
		},
		{name: "rls before policies", sql: "ALTER TABLE app.t ENABLE ROW LEVEL SECURITY;", want: []string{"RTL-C08"}},
		{
			name: "rls after policies", sql: "ALTER TABLE app.t ENABLE ROW LEVEL SECURITY;",
			policies: map[string]bool{"app.t": true},
		},
		{name: "policy first", sql: "CREATE POLICY p ON app.t USING (true);\nALTER TABLE app.t ENABLE ROW LEVEL SECURITY;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := codes(t, parser, tc.sql, tc.policies)
			if difference := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); difference != "" {
				t.Errorf("Steps() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestPlannedSteps(t *testing.T) {
	t.Parallel()

	step := func(id string, phase migrate.Phase, kind safety.Kind) authoring.Step {
		return authoring.Step{
			Step:       migrate.Step{ID: id, Sealed: true, Phase: phase, Statements: []string{"x"}},
			Operations: []safety.Operation{{Kind: kind, Relation: "app.t"}},
		}
	}

	file := authoring.File{Name: "1_x.sql", Steps: []authoring.Step{
		step("a", migrate.PhaseExpand, safety.CreateIndex),
		step("b", migrate.PhaseExpand, safety.RenameColumn),
		step("c", migrate.PhaseContract, safety.DropColumn),
	}}

	got := lint.Steps(&file, target(), nil)
	if len(got) != 1 || got[0].Code != "RTL-C01" || got[0].Where != "1_x.sql b" {
		t.Errorf("planned steps = %v", got)
	}
}

func BenchmarkSteps(b *testing.B) {
	parser := newParser(b)

	file, err := authoring.Parse(migrate.Source{Name: "1_x.sql", SQL: "-- ratel:step\n" +
		"ALTER TABLE app.t ADD COLUMN c int;\nALTER TABLE app.t VALIDATE CONSTRAINT k;\nSET lock_timeout = 0;"})
	if err != nil {
		b.Fatal(err)
	}

	authoring.Classify(&file, safety.NewClassifier(parser, target()), parser)

	for b.Loop() {
		lint.Steps(&file, target(), nil)
	}
}
