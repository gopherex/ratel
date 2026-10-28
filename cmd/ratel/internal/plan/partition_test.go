package plan_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func TestPartitionRecipes(t *testing.T) {
	t.Parallel()

	target := env(18, "app.p", "app.c", "app.d")
	target.Safety.Relations["app.p"] = safety.Relation{Partitioned: true, PartitionKey: []string{"k"}}
	target.Current.Objects = []introspect.Object{{Key: "constraint app.c.c_k_check", SQL: "x"}}

	withDefault := env(18, "app.p", "app.c")
	withDefault.Safety.Relations["app.p"] = safety.Relation{Partitioned: true, DefaultPartition: true}

	attach := diff.Change{
		Kind: diff.AttachPartition, Key: "table app.c", Table: "app.p",
		SQL: "ALTER TABLE app.p ATTACH PARTITION app.c FOR VALUES FROM (1) TO (5);",
	}
	detach := diff.Change{
		Kind: diff.DetachPartition, Key: "table app.c", Table: "app.p", SQL: "ALTER TABLE app.p DETACH PARTITION app.c;",
		After: "CREATE TABLE app.c (k integer);",
	}

	checkPlans(t, []planCase{
		{
			name: "attach with a bound check", changes: []diff.Change{attach}, env: target,
			want: []string{
				"expand short: ALTER TABLE app.c ADD CONSTRAINT c_ratel_bound CHECK (k IS NOT NULL AND k >= 1 AND k < 5) " +
					"NOT VALID;",
				"expand long: ALTER TABLE app.c VALIDATE CONSTRAINT c_ratel_bound;",
				"expand short: ALTER TABLE app.p ATTACH PARTITION app.c FOR VALUES FROM (1) TO (5); " +
					"ALTER TABLE app.c DROP CONSTRAINT c_ratel_bound;",
			},
		},
		{name: "attach scanning the default partition", changes: []diff.Change{attach}, env: withDefault},
		{
			name: "new partition",
			changes: []diff.Change{{
				Kind: diff.AttachPartition, Key: "table app.n", Table: "app.p",
				SQL: "CREATE TABLE app.n PARTITION OF app.p DEFAULT;",
			}},
			env: target, want: []string{"expand short: CREATE TABLE app.n PARTITION OF app.p DEFAULT;"},
		},
		{
			name: "detach concurrently", changes: []diff.Change{detach}, env: target,
			want: []string{
				"contract long: ALTER TABLE app.p DETACH PARTITION app.c CONCURRENTLY;",
				"contract short: ALTER TABLE app.c DROP CONSTRAINT IF EXISTS c_k_check1;",
			},
		},
		{
			name: "detach beside a default partition", changes: []diff.Change{detach}, env: withDefault,
			want: []string{"contract short: ALTER TABLE app.p DETACH PARTITION app.c;"},
		},
		{
			name: "dropped partition",
			changes: []diff.Change{diff.Dropped(introspect.Object{
				Key: "table app.c", SQL: "CREATE TABLE app.c PARTITION OF app.p DEFAULT;",
			})},
			env: withDefault,
			want: []string{
				"contract short: ALTER TABLE app.p DETACH PARTITION app.c;",
				"contract short: DROP TABLE app.c;",
			},
		},
	})
}

func TestBoundCheck(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		key   []string
		bound string
		want  string
		ok    bool
	}{
		{[]string{"k"}, "FOR VALUES FROM (1) TO (5)", "k IS NOT NULL AND k >= 1 AND k < 5", true},
		{[]string{"k"}, "FOR VALUES FROM (MINVALUE) TO (MAXVALUE)", "k IS NOT NULL", true},
		{[]string{"k"}, "FOR VALUES IN ('a', 'b')", "k IS NOT NULL AND k IN ('a', 'b')", true},
		{[]string{"k"}, "FOR VALUES IN (NULL)", "", false},
		{[]string{"k"}, "FOR VALUES WITH (modulus 2, remainder 0)", "", false},
		{[]string{"k"}, "DEFAULT", "", false},
		{[]string{"a", "b"}, "FOR VALUES FROM (1, 1) TO (2, 2)", "", false},
	} {
		t.Run(tc.bound, func(t *testing.T) {
			t.Parallel()

			if got, ok := plan.BoundCheck(tc.key, tc.bound); got != tc.want || ok != tc.ok {
				t.Errorf("boundCheck() = %q, %v", got, ok)
			}
		})
	}
}

func BenchmarkBoundCheck(b *testing.B) {
	for b.Loop() {
		plan.BoundCheck([]string{"k"}, "FOR VALUES FROM (1) TO (5)")
	}
}
