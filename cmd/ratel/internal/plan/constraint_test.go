package plan_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func constraint(kind diff.Kind, name, definition string) diff.Change {
	sql := "ALTER TABLE app.t ADD CONSTRAINT " + name + " " + definition + ";"

	return diff.Change{Kind: kind, Key: "constraint app.t." + name, Table: "app.t", SQL: sql, After: sql}
}

func TestConstraintRecipes(t *testing.T) {
	t.Parallel()

	partitioned := env(17, "app.t", "app.u")
	partitioned.Safety.Relations["app.t"] = safety.Relation{Partitioned: true}

	nullable := env(17, "app.t")
	nullable.Current.Columns = []introspect.Column{{Key: "app.t.id"}}

	checkPlans(t, []planCase{
		{
			name: "check", changes: []diff.Change{constraint(diff.AddCheck, "c", "CHECK ((a > 0))")}, env: env(18, "app.t"),
			want: []string{
				"expand short: ALTER TABLE app.t ADD CONSTRAINT c CHECK ((a > 0)) NOT VALID;",
				"contract long: ALTER TABLE app.t VALIDATE CONSTRAINT c;",
			},
		},
		{
			name: "check on a new table", changes: []diff.Change{constraint(diff.AddCheck, "c", "CHECK ((a > 0))")},
			env: env(18), want: []string{"expand short: ALTER TABLE app.t ADD CONSTRAINT c CHECK ((a > 0));"},
		},
		{
			name: "foreign key on a partitioned table before 18",
			changes: []diff.Change{
				constraint(diff.AddForeignKey, "f", "FOREIGN KEY (u) REFERENCES app.u(id)"),
			},
			env: partitioned,
		},
		{
			name: "unique", changes: []diff.Change{
				constraint(diff.AddUnique, "k", "UNIQUE NULLS NOT DISTINCT (a, b) INCLUDE (c) DEFERRABLE"),
			},
			env: env(18, "app.t"),
			want: []string{
				"expand long: CREATE UNIQUE INDEX CONCURRENTLY k ON app.t USING btree (a, b) INCLUDE (c) " +
					"NULLS NOT DISTINCT;",
				"contract short: ALTER TABLE app.t ADD CONSTRAINT k UNIQUE USING INDEX k DEFERRABLE;",
			},
		},
		{
			name:    "primary key on a nullable column before 18",
			changes: []diff.Change{constraint(diff.AddPrimaryKey, "t_pkey", "PRIMARY KEY (id)")},
			env:     nullable,
			want: []string{
				"expand short: ALTER TABLE app.t ADD CONSTRAINT t_id_ratel_not_null CHECK (id IS NOT NULL) NOT VALID;",
				"expand long: CREATE UNIQUE INDEX CONCURRENTLY t_pkey ON app.t USING btree (id);",
				"contract long: ALTER TABLE app.t VALIDATE CONSTRAINT t_id_ratel_not_null;",
				"contract short: ALTER TABLE app.t ALTER COLUMN id SET NOT NULL; " +
					"ALTER TABLE app.t DROP CONSTRAINT t_id_ratel_not_null;",
				"contract short: ALTER TABLE app.t ADD CONSTRAINT t_pkey PRIMARY KEY USING INDEX t_pkey;",
			},
		},
		{
			name: "exclusion", changes: []diff.Change{constraint(diff.AddExclusion, "x", "EXCLUDE USING gist (r WITH &&)")},
			env: env(18, "app.t"),
		},
		{
			name: "replaced check",
			changes: []diff.Change{
				diff.Dropped(introspect.Object{Key: "constraint app.t.c", SQL: "ALTER TABLE app.t ADD CONSTRAINT c CHECK (a);"}),
				diff.Created(introspect.Object{Key: "constraint app.t.c", SQL: "ALTER TABLE app.t ADD CONSTRAINT c CHECK (b);"}),
			},
			env: env(18, "app.t"),
			want: []string{
				"expand short: ALTER TABLE app.t ADD CONSTRAINT c_ratel_new CHECK (b) NOT VALID;",
				"contract long: ALTER TABLE app.t VALIDATE CONSTRAINT c_ratel_new;",
				"contract short: ALTER TABLE app.t DROP CONSTRAINT c; ALTER TABLE app.t RENAME CONSTRAINT c_ratel_new TO c;",
			},
		},
		{
			name: "set not null on 18",
			changes: []diff.Change{{
				Kind: diff.SetNotNull, Key: "column app.t.a", Table: "app.t", Column: "a",
				After: " CONSTRAINT t_a_not_null NOT NULL", SQL: "ALTER TABLE app.t ADD CONSTRAINT t_a_not_null NOT NULL a;",
			}},
			env: env(18, "app.t"),
			want: []string{
				"expand short: ALTER TABLE app.t ADD CONSTRAINT t_a_not_null NOT NULL a NOT VALID;",
				"contract long: ALTER TABLE app.t VALIDATE CONSTRAINT t_a_not_null;",
			},
		},
	})
}

func TestUniqueParts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		definition       string
		keyword          string
		columns          []string
		index, deferring string
		ok               bool
	}{
		{"PRIMARY KEY (id)", "PRIMARY KEY", []string{"id"}, "", "", true},
		{"UNIQUE (a) WITH (fillfactor='70')", "UNIQUE", []string{"a"}, " WITH (fillfactor='70')", "", true},
		{
			"UNIQUE NULLS NOT DISTINCT (a) INCLUDE (b) WITH (fillfactor='70') DEFERRABLE INITIALLY DEFERRED", "UNIQUE",
			[]string{"a"},
			" INCLUDE (b) NULLS NOT DISTINCT WITH (fillfactor='70')", " DEFERRABLE INITIALLY DEFERRED", true,
		},
		{"UNIQUE (a) USING INDEX TABLESPACE x", "UNIQUE", []string{"a"}, "", "", false},
		{"CHECK (a)", "", nil, "", "", false},
	} {
		t.Run(tc.definition, func(t *testing.T) {
			t.Parallel()

			parts, ok := plan.UniqueParts(tc.definition)
			if ok != tc.ok || !ok {
				if ok != tc.ok {
					t.Errorf("uniqueParts() ok = %v", ok)
				}

				return
			}

			want := plan.UniqueDefinition{
				Keyword: tc.keyword, Columns: tc.columns, Index: tc.index,
				Deferrable: tc.deferring,
			}
			if difference := cmp.Diff(want, parts); difference != "" {
				t.Errorf("uniqueParts() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestConstraintText(t *testing.T) {
	t.Parallel()

	sql := `ALTER TABLE app.t ADD CONSTRAINT "Odd name" FOREIGN KEY (u) REFERENCES app.u(id);`

	if got := plan.ConstraintDefinition(sql, `"Odd name"`); got != "FOREIGN KEY (u) REFERENCES app.u(id)" {
		t.Errorf("constraintDefinition() = %q", got)
	}

	if got := plan.Referenced(sql); got != "app.u" {
		t.Errorf("referenced() = %q", got)
	}

	if got := plan.Referenced("ALTER TABLE app.t ADD CONSTRAINT c CHECK (a);"); got != "" {
		t.Errorf("referenced() of a check = %q", got)
	}

	for clause, want := range map[string]string{
		" CONSTRAINT x NOT NULL": "x", " NOT NULL": "t_a_not_null",
	} {
		if got := plan.NotNullName("app.t", "a", clause); got != want {
			t.Errorf("notNullName(%q) = %q", clause, got)
		}
	}
}

func BenchmarkUniqueParts(b *testing.B) {
	for b.Loop() {
		plan.UniqueParts("UNIQUE NULLS NOT DISTINCT (a, b) INCLUDE (c) WITH (fillfactor='70') DEFERRABLE")
	}
}
