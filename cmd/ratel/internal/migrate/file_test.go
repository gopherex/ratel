package migrate_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func planned() plan.Plan {
	index := safety.Operation{Kind: safety.CreateIndexConcurrently, Relation: `app."Odd, table"`}
	fill := safety.Operation{Kind: safety.Backfill, Relation: "app.t", Rewrite: true}

	return plan.Plan{Steps: []plan.Step{
		{
			Phase: plan.Expand, Class: plan.Long,
			Statements: []plan.Statement{{
				SQL:        `CREATE INDEX CONCURRENTLY i ON app."Odd, table" (a);`,
				Operations: []safety.Operation{index},
			}},
			Safety: safety.Safety{
				Tx: safety.OwnStatement, Locks: []safety.RelLock{{Relation: `app."Odd, table"`, Mode: safety.ShareUpdateExclusive}},
			},
			Allow: []string{"RTL-C01", "RTL-L02"},
		},
		{
			Phase: plan.Backfill, Class: plan.Batched,
			Statements: []plan.Statement{{
				SQL:        "UPDATE app.t SET f = 'x;y' WHERE f IS NULL;",
				Operations: []safety.Operation{fill},
			}},
			Fill: &plan.Fill{Table: "app.t", Key: []string{"a", "b"}},
		},
	}}
}

func TestWriteParse(t *testing.T) {
	t.Parallel()

	name := migrate.FileName(time.Date(2026, 10, 29, 1, 2, 3, 0, time.UTC), "add_index")
	if name != "20261029010203_add_index.sql" {
		t.Fatalf("FileName() = %q", name)
	}

	written := string(migrate.Write(name, planned(), 18))

	file, err := migrate.Parse(name, written)
	if err != nil {
		t.Fatal(err)
	}

	want := []migrate.Step{
		{
			ID:    migrate.StepID(name, []string{`CREATE INDEX CONCURRENTLY i ON app."Odd, table" (a)`}),
			Phase: plan.Expand, Class: plan.Long, Tx: safety.OwnStatement, Major: 18, Planned: true,
			Locks:      []safety.RelLock{{Relation: `app."Odd, table"`, Mode: safety.ShareUpdateExclusive}},
			Allow:      []string{"RTL-C01", "RTL-L02"},
			Operations: []safety.Operation{{Kind: safety.CreateIndexConcurrently, Relation: `app."Odd, table"`}},
			Statements: []string{`CREATE INDEX CONCURRENTLY i ON app."Odd, table" (a)`},
		},
		{
			ID:    migrate.StepID(name, []string{"UPDATE app.t SET f = 'x;y' WHERE f IS NULL"}),
			Phase: plan.Backfill, Class: plan.Batched, Major: 18, Planned: true, Key: []string{"a", "b"},
			Operations: []safety.Operation{{Kind: safety.Backfill, Relation: "app.t", Rewrite: true}},
			Statements: []string{"UPDATE app.t SET f = 'x;y' WHERE f IS NULL"},
		},
	}

	if difference := cmp.Diff(want, file.Steps); difference != "" {
		t.Errorf("Parse(Write()) (-want +got):\n%s\n%s", difference, written)
	}
}

func TestParseHandWritten(t *testing.T) {
	t.Parallel()

	sql := `-- a hand-written migration
CREATE TABLE app.t (id int);
-- @ratel.allow RTL-D01 reason=cleanup
DROP TABLE app.old;
-- ratel:step phase=backfill
UPDATE app.t SET id = id;
UPDATE app.t SET id = id + 0;
`

	file, err := migrate.Parse("1_hand.sql", sql)
	if err != nil {
		t.Fatal(err)
	}

	got := make([][]string, 0, len(file.Steps))
	for _, step := range file.Steps {
		got = append(got, append([]string{string(step.Phase)}, append(step.Allow, step.Statements...)...))
	}

	want := [][]string{
		{"expand", "CREATE TABLE app.t (id int)"},
		{"expand", "RTL-D01", "DROP TABLE app.old"},
		{"backfill", "UPDATE app.t SET id = id", "UPDATE app.t SET id = id + 0"},
	}
	if difference := cmp.Diff(want, got); difference != "" {
		t.Errorf("Parse() (-want +got):\n%s", difference)
	}

	if file.Steps[2].Planned || file.Steps[0].ID == "" {
		t.Errorf("hand-written steps = %+v", file.Steps)
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{
		"-- ratel:step phase=later\nSELECT 1;",
		"-- ratel:step tx=maybe\nSELECT 1;",
		"-- ratel:step locks=app.t\nSELECT 1;",
		"-- ratel:step locks=app.t:Huge\nSELECT 1;",
		"-- ratel:step pg=x\nSELECT 1;",
		"-- ratel:step ops=a:b\nSELECT 1;",
		"-- ratel:step color=red\nSELECT 1;",
		"-- ratel:step bare\nSELECT 1;",
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			if _, err := migrate.Parse("x.sql", sql); err == nil {
				t.Errorf("Parse(%q) accepted", sql)
			}
		})
	}
}

func BenchmarkWriteParse(b *testing.B) {
	planned := planned()

	for b.Loop() {
		if _, err := migrate.Parse("x.sql", string(migrate.Write("x.sql", planned, 18))); err != nil {
			b.Fatal(err)
		}
	}
}
