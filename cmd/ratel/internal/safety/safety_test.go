package safety_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func existing(relations ...string) safety.Env {
	env := safety.Env{Major: 18, Relations: make(map[string]safety.Relation)}
	for _, relation := range relations {
		env.Relations[relation] = safety.Relation{}
	}

	return env
}

func TestAssess(t *testing.T) {
	t.Parallel()

	env := existing("app.t", "app.r")
	env.Created = map[string]bool{"app.r": true}

	for _, tc := range []struct {
		name       string
		operations []safety.Operation
		env        safety.Env
		want       safety.Safety
	}{
		{name: "nothing", want: safety.Safety{}},
		{
			name:       "index build",
			operations: []safety.Operation{{Kind: safety.CreateIndex, Relation: "app.t"}},
			env:        env,
			want: safety.Safety{
				Locks: []safety.RelLock{{"app.t", safety.Share}}, Effect: safety.IndexBuild,
				Reasons: []string{"RTL-L02"},
			},
		},
		{
			name: "strictest lock first",
			operations: []safety.Operation{
				{Kind: safety.ValidateConstraint, Relation: "app.t"},
				{Kind: safety.AddForeignKeyNotValid, Relation: "app.t", Other: "app.x"},
				{Kind: safety.SetDefault, Relation: "app.t"},
			},
			env: existing("app.t", "app.x"),
			want: safety.Safety{
				Locks:  []safety.RelLock{{"app.t", safety.AccessExclusive}, {"app.x", safety.ShareRowExclusive}},
				Effect: safety.Scan, Compat: safety.Conditional, Reasons: []string{"RTL-C06"},
			},
		},
		{
			name:       "probed rewrite",
			operations: []safety.Operation{{Kind: safety.AlterColumnType, Relation: "app.t", Rewrite: true}},
			env:        env,
			want: safety.Safety{
				Locks: []safety.RelLock{{"app.t", safety.AccessExclusive}}, Effect: safety.Rewrite,
				Compat: safety.Breaking, Reasons: []string{"RTL-C04", "RTL-R02"},
			},
		},
		{
			name:       "created in the plan",
			operations: []safety.Operation{{Kind: safety.CreateIndex, Relation: "app.r"}},
			env:        env,
			want:       safety.Safety{},
		},
		{
			name:       "concurrent",
			operations: []safety.Operation{{Kind: safety.CreateIndexConcurrently, Relation: "app.t"}},
			env:        env,
			want: safety.Safety{
				Locks: []safety.RelLock{{"app.t", safety.ShareUpdateExclusive}}, Effect: safety.IndexBuild,
				Tx: safety.OwnStatement, Waits: true,
			},
		},
		{
			name:       "classifier codes",
			operations: []safety.Operation{{Kind: safety.AddColumn, Relation: "app.t", Codes: []string{"RTL-C03"}}},
			env:        env,
			want: safety.Safety{
				Locks: []safety.RelLock{{"app.t", safety.AccessExclusive}}, Reasons: []string{"RTL-C03"},
			},
		},
		{
			name:       "drop",
			operations: []safety.Operation{{Kind: safety.DropTable, Relation: "app.t"}},
			env:        env,
			want: safety.Safety{
				Locks: []safety.RelLock{{"app.t", safety.AccessExclusive}}, Destroys: safety.Data,
				Compat: safety.Breaking, Reasons: []string{"RTL-C02", "RTL-D01"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if difference := cmp.Diff(tc.want, safety.Assess(tc.operations, tc.env)); difference != "" {
				t.Errorf("Assess() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestGrantLocksFrom18(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		major int
		want  []safety.RelLock
	}{
		{major: 17},
		{major: 18, want: []safety.RelLock{{"app.t", safety.AccessShare}}},
	} {
		t.Run(cmp.Diff(nil, tc.want), func(t *testing.T) {
			t.Parallel()

			env := existing("app.t")
			env.Major = tc.major

			got := safety.Assess([]safety.Operation{{Kind: safety.Grant, Relation: "app.t"}}, env)
			if difference := cmp.Diff(tc.want, got.Locks); difference != "" {
				t.Errorf("locks on %d (-want +got):\n%s", tc.major, difference)
			}
		})
	}
}

func TestLockMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode   safety.LockMode
		name   string
		blocks bool
	}{
		{safety.NoLock, "None", false},
		{safety.AccessShare, "AccessShare", false},
		{safety.RowShare, "RowShare", false},
		{safety.RowExclusive, "RowExclusive", false},
		{safety.ShareUpdateExclusive, "ShareUpdateExclusive", false},
		{safety.Share, "Share", true},
		{safety.ShareRowExclusive, "ShareRowExclusive", true},
		{safety.Exclusive, "Exclusive", true},
		{safety.AccessExclusive, "AccessExclusive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.mode.String() != tc.name || tc.mode.Blocks() != tc.blocks {
				t.Errorf("%d: %s, blocks %v", tc.mode, tc.mode, tc.mode.Blocks())
			}
		})
	}
}

// TestConflicts checks the table against PostgreSQL's documented matrix.
func TestConflicts(t *testing.T) {
	t.Parallel()

	// Each mode's row: the modes it conflicts with, weakest first.
	matrix := map[safety.LockMode]string{
		safety.NoLock:               "--------",
		safety.AccessShare:          "-------X",
		safety.RowShare:             "------XX",
		safety.RowExclusive:         "----XXXX",
		safety.ShareUpdateExclusive: "---XXXXX",
		safety.Share:                "--XX-XXX",
		safety.ShareRowExclusive:    "--XXXXXX",
		safety.Exclusive:            "-XXXXXXX",
		safety.AccessExclusive:      "XXXXXXXX",
	}

	for mode, row := range matrix {
		for other := safety.AccessShare; other <= safety.AccessExclusive; other++ {
			if want := row[other-1] == 'X'; mode.Conflicts(other) != want {
				t.Errorf("%s vs %s = %v", mode, other, !want)
			}
		}

		if mode.Conflicts(safety.NoLock) {
			t.Errorf("%s conflicts with no lock", mode)
		}
	}
}

func TestEffectString(t *testing.T) {
	t.Parallel()

	for effect, want := range map[safety.Effect]string{
		safety.Metadata: "metadata", safety.Scan: "scan", safety.IndexBuild: "index build", safety.Rewrite: "rewrite",
	} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()

			if effect.String() != want {
				t.Errorf("%d.String() = %q", effect, effect)
			}
		})
	}
}

func TestExists(t *testing.T) {
	t.Parallel()

	env := existing("app.t", "app.new")
	env.Created = map[string]bool{"app.new": true}

	for relation, want := range map[string]bool{"app.t": true, "app.new": false, "app.x": false} {
		t.Run(relation, func(t *testing.T) {
			t.Parallel()

			if env.Exists(relation) != want {
				t.Errorf("Exists(%q) = %v", relation, !want)
			}
		})
	}
}

func BenchmarkAssess(b *testing.B) {
	env := existing("app.t", "app.x")
	operations := []safety.Operation{
		{Kind: safety.ValidateConstraint, Relation: "app.t"},
		{Kind: safety.AddForeignKeyNotValid, Relation: "app.t", Other: "app.x"},
		{Kind: safety.AlterColumnType, Relation: "app.t", Rewrite: true},
	}

	for b.Loop() {
		safety.Assess(operations, env)
	}
}
