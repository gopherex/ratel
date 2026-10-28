package plan_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

func TestObjectRecipes(t *testing.T) {
	t.Parallel()

	checkPlans(t, []planCase{
		{
			name: "enum values",
			changes: []diff.Change{
				{Kind: diff.RenameEnumValue, Key: "value app.e 'b'", SQL: "ALTER TYPE app.e RENAME VALUE 'a' TO 'b';"},
				{Kind: diff.AddEnumValue, Key: "value app.e 'c'", SQL: "ALTER TYPE app.e ADD VALUE 'c' AFTER 'b';"},
				{Kind: diff.RemoveEnumValue, Key: "type app.f"},
			},
			env: env(18),
			want: []string{
				"expand short: ALTER TYPE app.e RENAME VALUE 'a' TO 'b';",
				"expand short: ALTER TYPE app.e ADD VALUE 'c' AFTER 'b';",
			},
		},
		{
			name: "domain check",
			changes: []diff.Change{{
				Kind: diff.AddDomainCheck, Key: "type app.d", SQL: "ALTER DOMAIN app.d ADD CONSTRAINT c CHECK (VALUE > 0);",
			}},
			env: env(18),
			want: []string{
				"expand short: ALTER DOMAIN app.d ADD CONSTRAINT c CHECK (VALUE > 0) NOT VALID;",
				"contract long: ALTER DOMAIN app.d VALIDATE CONSTRAINT c;",
			},
		},
		{
			name: "sequence and routine",
			changes: []diff.Change{
				{Kind: diff.AlterSequence, Key: "sequence app.s", SQL: "ALTER SEQUENCE app.s CYCLE;"},
				{Kind: diff.DropRoutine, Key: "routine app.f()", SQL: "DROP FUNCTION app.f();"},
			},
			env: env(18, "app.s"),
			want: []string{
				"expand short: ALTER SEQUENCE app.s CYCLE;",
				"contract short: DROP FUNCTION app.f();",
			},
		},
		{
			name: "renamed table",
			changes: []diff.Change{
				{Kind: diff.RenameTable, Key: "table app.new", Table: "app.old", SQL: "ALTER TABLE app.old RENAME TO new;"},
				{Kind: diff.CreateIndex, Key: "index app.i", Table: "app.new", SQL: "CREATE INDEX i ON app.new (a);"},
			},
			env: env(18, "app.old"),
			want: []string{
				"expand short: ALTER TABLE app.old RENAME TO new;",
				"expand long: CREATE INDEX CONCURRENTLY i ON app.new (a);",
			},
		},
	})
}

func TestRenameAllowed(t *testing.T) {
	t.Parallel()

	planned := plan.Build([]diff.Change{{
		Kind: diff.RenameTable, Key: "table app.new", Table: "app.old", SQL: "ALTER TABLE app.old RENAME TO new;",
	}}, env(18, "app.old"), plan.Intents{})

	if len(planned.Steps) != 1 || len(planned.Steps[0].Allow) != 1 || planned.Steps[0].Allow[0] != "RTL-C01" {
		t.Fatalf("rename = %+v", planned.Steps)
	}
}

func BenchmarkObjectRecipes(b *testing.B) {
	changes := []diff.Change{
		{Kind: diff.CreateSchema, Key: "schema app", SQL: "CREATE SCHEMA app;"},
		{Kind: diff.AddEnumValue, Key: "value app.e 'c'", SQL: "ALTER TYPE app.e ADD VALUE 'c';"},
	}

	for b.Loop() {
		plan.Build(changes, env(18), plan.Intents{})
	}
}
