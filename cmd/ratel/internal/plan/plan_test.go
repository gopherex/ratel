package plan_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// env is a target on a major version holding the relations.
func env(major int, relations ...string) *plan.Env {
	result := &plan.Env{
		Safety:   safety.Env{Major: major, Relations: make(map[string]safety.Relation)},
		Rewrites: make(map[string]bool), Reindexes: make(map[string]bool),
	}

	for _, relation := range relations {
		result.Safety.Relations[relation] = safety.Relation{}
	}

	return result
}

// steps is each step as "phase class: statements".
func steps(planned plan.Plan) []string {
	result := make([]string, 0, len(planned.Steps))

	for index := range planned.Steps {
		step := &planned.Steps[index]
		parts := make([]string, 0, 1+len(step.Statements))
		parts = append(parts, string(step.Phase)+" "+string(step.Class)+":")

		for _, statement := range step.Statements {
			parts = append(parts, statement.SQL)
		}

		result = append(result, strings.Join(parts, " "))
	}

	return result
}

type planCase struct {
	name    string
	changes []diff.Change
	env     *plan.Env
	intents plan.Intents
	want    []string
}

func checkPlans(t *testing.T, cases []planCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := steps(plan.Build(tc.changes, tc.env, tc.intents))
			if difference := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); difference != "" {
				t.Errorf("Build() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	index := func(sql string) diff.Change {
		return diff.Change{Kind: diff.CreateIndex, Key: "index app.i", Table: "app.t", SQL: sql, After: sql}
	}

	checkPlans(t, []planCase{
		{
			name: "phases in order",
			changes: []diff.Change{
				{Kind: diff.DropTable, Key: "table app.old", Table: "app.old", SQL: "DROP TABLE app.old;"},
				{Kind: diff.CreateSchema, Key: "schema app", SQL: "CREATE SCHEMA app;"},
			},
			env: env(18, "app.old"),
			want: []string{
				"expand short: CREATE SCHEMA app;",
				"contract short: DROP TABLE app.old;",
			},
		},
		{
			name: "replaced trigger",
			changes: []diff.Change{
				diff.Dropped(introspect.Object{Key: "trigger app.t.x", SQL: "CREATE TRIGGER x"}),
				diff.Created(introspect.Object{Key: "trigger app.t.x", SQL: "CREATE TRIGGER x AFTER UPDATE"}),
			},
			env:  env(18, "app.t"),
			want: []string{"contract short: DROP TRIGGER x ON app.t; CREATE TRIGGER x AFTER UPDATE"},
		},
		{
			name: "replaced index",
			changes: []diff.Change{
				diff.Dropped(introspect.Object{Key: "index app.i", SQL: "CREATE INDEX i ON app.t USING btree (a);"}),
				index("CREATE INDEX i ON app.t USING btree (b);"),
			},
			env: env(18, "app.t"),
			want: []string{
				"expand long: CREATE INDEX CONCURRENTLY i_ratel_new ON app.t USING btree (b);",
				"contract long: DROP INDEX CONCURRENTLY app.i;",
				"contract short: ALTER INDEX app.i_ratel_new RENAME TO i;",
			},
		},
	})
}

func TestStepsAssessed(t *testing.T) {
	t.Parallel()

	changes := []diff.Change{
		{Kind: diff.CreateTable, Key: "table app.n", Table: "app.n", SQL: "CREATE TABLE app.n ();"},
		{Kind: diff.CreateIndex, Key: "index app.i", Table: "app.n", SQL: "CREATE INDEX i ON app.n (a);"},
	}

	planned := plan.Build(changes, env(18), plan.Intents{})
	if len(planned.Steps) != 2 || planned.Steps[1].Safety.Effect != safety.Metadata ||
		len(planned.Steps[1].Safety.Locks) != 0 {
		t.Fatalf("index on a new table = %+v", planned.Steps)
	}
}

func TestAllow(t *testing.T) {
	t.Parallel()

	changes := []diff.Change{{
		Kind: diff.AlterPersistence, Key: "table app.t", Table: "app.t", SQL: "ALTER TABLE app.t SET LOGGED;",
	}}

	for _, tc := range []struct {
		name  string
		allow map[string][]string
		steps int
	}{
		{name: "refused"},
		{name: "allowed on the table", allow: map[string][]string{"table app.t": {"RTL-R03"}}, steps: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			planned := plan.Build(changes, env(18, "app.t"), plan.Intents{Allow: tc.allow})
			if len(planned.Steps) != tc.steps || len(planned.Findings) != 1 || planned.Findings[0].Fixed {
				t.Fatalf("Build() = %+v", planned)
			}

			if tc.steps == 1 && cmp.Diff([]string{"RTL-R03"}, planned.Steps[0].Allow) != "" {
				t.Errorf("allow = %v", planned.Steps[0].Allow)
			}
		})
	}
}

func BenchmarkBuild(b *testing.B) {
	target := env(18, "app.t")
	changes := []diff.Change{
		{
			Kind: diff.AddCheck, Key: "constraint app.t.c", Table: "app.t",
			SQL: "ALTER TABLE app.t ADD CONSTRAINT c CHECK (a > 0);",
		},
		{Kind: diff.CreateIndex, Key: "index app.i", Table: "app.t", SQL: "CREATE INDEX i ON app.t USING btree (a);"},
		{
			Kind: diff.SetNotNull, Key: "column app.t.a", Table: "app.t", Column: "a",
			SQL: "ALTER TABLE app.t ALTER COLUMN a SET NOT NULL;",
		},
	}

	for b.Loop() {
		plan.Build(changes, target, plan.Intents{})
	}
}
