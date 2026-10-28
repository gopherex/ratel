package plan_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

// keyed is a target table app.t with primary key id and the columns.
func keyed(major int, current, desired []introspect.Column) *plan.Env {
	target := env(major, "app.t")
	target.Current.Objects = []introspect.Object{
		{Key: "constraint app.t.t_pkey", SQL: "ALTER TABLE app.t ADD CONSTRAINT t_pkey PRIMARY KEY (id);"},
	}
	target.Current.Columns = current
	target.Desired.Columns = desired

	return target
}

func TestColumnRecipes(t *testing.T) {
	t.Parallel()

	add := diff.Change{
		Kind: diff.AddColumn, Key: "column app.t.f", Table: "app.t", Column: "f",
		SQL: "ALTER TABLE app.t ADD COLUMN f text NOT NULL;",
	}
	filled := keyed(17, nil, []introspect.Column{{Key: "app.t.f", Type: "text", NotNull: " NOT NULL"}})
	volatile := keyed(18, nil, []introspect.Column{{Key: "app.t.f", Type: "double precision", Default: "random()"}})
	volatile.Rewrites["column app.t.f"] = true

	checkPlans(t, []planCase{
		{
			name: "nullable", env: keyed(18, nil, []introspect.Column{{Key: "app.t.f", Type: "text"}}),
			changes: []diff.Change{{
				Kind: diff.AddColumn, Key: "column app.t.f", Table: "app.t", Column: "f",
				SQL: "ALTER TABLE app.t ADD COLUMN f text;",
			}},
			want: []string{"expand short: ALTER TABLE app.t ADD COLUMN f text;"},
		},
		{name: "not null without backfill", changes: []diff.Change{add}, env: filled},
		{
			name: "not null backfilled", changes: []diff.Change{add}, env: filled,
			intents: plan.Intents{Backfill: map[string]string{"column app.t.f": "'x'"}},
			want: []string{
				"expand short: ALTER TABLE app.t ADD COLUMN f text;",
				"backfill backfill: UPDATE app.t SET f = 'x' WHERE f IS NULL;",
				"contract short: ALTER TABLE app.t ADD CONSTRAINT t_f_ratel_not_null CHECK (f IS NOT NULL) NOT VALID;",
				"contract long: ALTER TABLE app.t VALIDATE CONSTRAINT t_f_ratel_not_null;",
				"contract short: ALTER TABLE app.t ALTER COLUMN f SET NOT NULL; " +
					"ALTER TABLE app.t DROP CONSTRAINT t_f_ratel_not_null;",
			},
		},
		{
			name: "volatile default backfilled", env: volatile,
			changes: []diff.Change{{
				Kind: diff.AddColumn, Key: "column app.t.f", Table: "app.t", Column: "f",
				SQL: "ALTER TABLE app.t ADD COLUMN f double precision DEFAULT random();",
			}},
			intents: plan.Intents{Backfill: map[string]string{"column app.t.f": "0"}},
			want: []string{
				"expand short: ALTER TABLE app.t ADD COLUMN f double precision; " +
					"ALTER TABLE app.t ALTER COLUMN f SET DEFAULT random();",
				"backfill backfill: UPDATE app.t SET f = 0 WHERE f IS NULL;",
			},
		},
		{
			name: "virtual expression", env: env(18, "app.t"),
			changes: []diff.Change{{
				Kind: diff.AlterGenerated, Key: "column app.t.g", Table: "app.t", Column: "g",
				After: " GENERATED ALWAYS AS (1) VIRTUAL", SQL: "ALTER TABLE app.t ALTER COLUMN g SET EXPRESSION AS (1);",
			}},
			want: []string{"expand short: ALTER TABLE app.t ALTER COLUMN g SET EXPRESSION AS (1);"},
		},
		{
			name: "stored expression", env: env(18, "app.t"),
			changes: []diff.Change{{
				Kind: diff.AlterGenerated, Key: "column app.t.g", Table: "app.t", Column: "g",
				After: " GENERATED ALWAYS AS (1) STORED", SQL: "ALTER TABLE app.t ALTER COLUMN g SET EXPRESSION AS (1);",
			}},
		},
		{
			name: "plain", env: env(18, "app.t"),
			changes: []diff.Change{
				{
					Kind: diff.DropDefault, Key: "column app.t.a", Table: "app.t",
					SQL: "ALTER TABLE app.t ALTER COLUMN a DROP DEFAULT;",
				},
				{Kind: diff.DropColumn, Key: "column app.t.b", Table: "app.t", SQL: "ALTER TABLE app.t DROP COLUMN b;"},
			},
			want: []string{
				"contract short: ALTER TABLE app.t ALTER COLUMN a DROP DEFAULT;",
				"contract short: ALTER TABLE app.t DROP COLUMN b;",
			},
		},
	})
}

func TestRenameColumn(t *testing.T) {
	t.Parallel()

	target := keyed(18, []introspect.Column{{Key: "app.t.v", Type: "integer"}},
		[]introspect.Column{{Key: "app.t.w", Type: "integer"}})
	changes := []diff.Change{
		{
			Kind: diff.RenameColumn, Key: "column app.t.w", Table: "app.t", Column: "w",
			SQL: "ALTER TABLE app.t RENAME COLUMN v TO w;",
		},
		{
			Kind: diff.SetDefault, Key: "column app.t.w", Table: "app.t", Column: "w",
			SQL: "ALTER TABLE app.t ALTER COLUMN w SET DEFAULT 1;",
		},
	}

	planned := plan.Build(changes, target, plan.Intents{})
	got := steps(planned)

	if len(got) != 3 || got[1] != "backfill backfill: UPDATE app.t SET w = v WHERE w IS DISTINCT FROM v;" ||
		got[2] != "contract short: DROP TRIGGER t_w_ratel_sync ON app.t; DROP FUNCTION app.t_w_ratel_sync(); "+
			"ALTER TABLE app.t DROP COLUMN v;" {
		t.Errorf("rename = %q", got)
	}
}

func BenchmarkAddColumn(b *testing.B) {
	target := keyed(18, nil, []introspect.Column{{Key: "app.t.f", Type: "text", NotNull: " NOT NULL"}})
	changes := []diff.Change{{
		Kind: diff.AddColumn, Key: "column app.t.f", Table: "app.t", Column: "f",
		SQL: "ALTER TABLE app.t ADD COLUMN f text NOT NULL;",
	}}
	intents := plan.Intents{Backfill: map[string]string{"column app.t.f": "'x'"}}

	for b.Loop() {
		plan.Build(changes, target, intents)
	}
}
