package plan_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func TestIndexRecipes(t *testing.T) {
	t.Parallel()

	partitioned := env(18, "app.p", "app.p1", "app.p2", "app.p21")
	partitioned.Safety.Relations["app.p"] = safety.Relation{Partitioned: true, Partitions: []string{"app.p1", "app.p2"}}
	partitioned.Safety.Relations["app.p2"] = safety.Relation{Partitioned: true, Partitions: []string{"app.p21"}}

	create := func(table string) diff.Change {
		sql := "CREATE INDEX i ON ONLY " + table + " USING btree (v);"

		return diff.Change{Kind: diff.CreateIndex, Key: "index app.i", Table: table, SQL: sql, After: sql}
	}

	checkPlans(t, []planCase{
		{
			name: "partitioned", changes: []diff.Change{create("app.p")}, env: partitioned,
			want: []string{
				"expand short: CREATE INDEX i ON ONLY app.p USING btree (v);",
				"expand long: CREATE INDEX CONCURRENTLY p1_i ON app.p1 USING btree (v);",
				"expand short: ALTER INDEX app.i ATTACH PARTITION app.p1_i;",
				"expand short: CREATE INDEX p2_i ON ONLY app.p2 USING btree (v);",
				"expand long: CREATE INDEX CONCURRENTLY p21_p2_i ON app.p21 USING btree (v);",
				"expand short: ALTER INDEX app.p2_i ATTACH PARTITION app.p21_p2_i;",
				"expand short: ALTER INDEX app.i ATTACH PARTITION app.p2_i;",
			},
		},
		{
			name:    "drop on a partitioned table",
			changes: []diff.Change{{Kind: diff.DropIndex, Key: "index app.i", Table: "app.p", SQL: "DROP INDEX app.i;"}},
			env:     partitioned,
			want:    []string{"contract short: DROP INDEX app.i;"},
		},
		{
			name: "rename",
			changes: []diff.Change{{
				Kind: diff.RenameIndex, Key: "index app.j", Table: "app.t", SQL: "ALTER INDEX app.i RENAME TO j;",
			}},
			env:  env(18, "app.t"),
			want: []string{"expand short: ALTER INDEX app.i RENAME TO j;"},
		},
	})
}

func BenchmarkPartitionedIndex(b *testing.B) {
	target := env(18, "app.p", "app.p1")
	target.Safety.Relations["app.p"] = safety.Relation{Partitioned: true, Partitions: []string{"app.p1"}}
	changes := []diff.Change{{
		Kind: diff.CreateIndex, Key: "index app.i", Table: "app.p", SQL: "CREATE INDEX i ON ONLY app.p USING btree (v);",
	}}

	for b.Loop() {
		plan.Build(changes, target, plan.Intents{})
	}
}
