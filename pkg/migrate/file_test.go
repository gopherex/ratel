package migrate_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/pkg/migrate"
)

func sealedSteps(name string) []migrate.Step {
	index := `CREATE INDEX CONCURRENTLY i ON app."Odd, table" (a)`
	fill := "UPDATE app.t SET f = 'x;y' WHERE f IS NULL"

	return []migrate.Step{
		{
			ID:    migrate.StepID(name, []string{index}),
			Phase: migrate.PhaseExpand, Class: migrate.ClassLong, Tx: migrate.OwnStatement, Major: 18, Sealed: true,
			Locks:      []migrate.RelLock{{Relation: `app."Odd, table"`, Mode: migrate.ShareUpdateExclusive}},
			Allow:      []string{"RTL-C01", "RTL-L02"},
			Ops:        "create-index-concurrently:app.%22Odd%2C+table%22::false",
			Statements: []string{index},
		},
		{
			ID:    migrate.StepID(name, []string{fill}),
			Phase: migrate.PhaseBackfill, Class: migrate.ClassBatched, Major: 18, Sealed: true, Hand: true,
			Key: []string{"a", "b"}, Statements: []string{fill},
		},
	}
}

// render writes steps as a migration file.
func render(steps []migrate.Step) string {
	var text strings.Builder

	for index := range steps {
		text.WriteString(steps[index].Header() + "\n")

		for _, statement := range steps[index].Statements {
			text.WriteString(statement + ";\n")
		}
	}

	return text.String()
}

func TestHeaderParse(t *testing.T) {
	t.Parallel()

	name := migrate.FileName(time.Date(2026, 10, 29, 1, 2, 3, 0, time.UTC), "add_index")
	if name != "20261029010203_add_index.sql" {
		t.Fatalf("FileName() = %q", name)
	}

	want := sealedSteps(name)

	file, err := migrate.Parse(name, render(want))
	if err != nil {
		t.Fatal(err)
	}

	if difference := cmp.Diff(want, file.Steps, cmp.AllowUnexported(migrate.Step{}),
		cmp.FilterPath(func(path cmp.Path) bool {
			field := path.Last().String()

			return field == ".at" || field == ".headed"
		}, cmp.Ignore())); difference != "" {
		t.Errorf("Parse(Header()) (-want +got):\n%s", difference)
	}

	if !file.Steps[0].Planned() || file.Steps[1].Planned() {
		t.Errorf("Planned() = %v, %v", file.Steps[0].Planned(), file.Steps[1].Planned())
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

	if file.Steps[2].Sealed || file.Steps[2].Planned() || file.Steps[0].ID == "" {
		t.Errorf("hand-written steps = %+v", file.Steps)
	}
}

func TestSeal(t *testing.T) {
	t.Parallel()

	sql := `-- a hand-written migration
CREATE TABLE app.t (id int);

-- @ratel.allow RTL-D01 reason=cleanup
DROP TABLE app.old;
-- ratel:step phase=backfill
UPDATE app.t SET id = id;
`

	file, err := migrate.Parse("1_hand.sql", sql)
	if err != nil {
		t.Fatal(err)
	}

	for index := range file.Steps {
		step := &file.Steps[index]
		step.Sealed, step.Hand, step.Major = true, true, 18

		step.Class = migrate.ClassShort
		if step.Phase == migrate.PhaseBackfill {
			step.Class = migrate.ClassBatched
		}
	}

	sealed := migrate.Seal(sql, file)
	want := `-- a hand-written migration
` + file.Steps[0].Header() + `
CREATE TABLE app.t (id int);

-- @ratel.allow RTL-D01 reason=cleanup
` + file.Steps[1].Header() + `
DROP TABLE app.old;
` + file.Steps[2].Header() + `
UPDATE app.t SET id = id;
`

	if sealed != want {
		t.Fatalf("Seal() =\n%s\nwant\n%s", sealed, want)
	}

	again, err := migrate.Parse("1_hand.sql", sealed)
	if err != nil {
		t.Fatal(err)
	}

	for index := range again.Steps {
		step := &again.Steps[index]
		if !step.Sealed || !step.Hand || step.ID != file.Steps[index].ID ||
			!cmp.Equal(step.Allow, file.Steps[index].Allow) {
			t.Errorf("sealed step %d = %+v", index, step)
		}
	}

	if resealed := migrate.Seal(sealed, again); resealed != sealed {
		t.Errorf("Seal() is not idempotent:\n%s", resealed)
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{
		"-- ratel:step phase=later\nSELECT 1;",
		"-- ratel:step tx=maybe\nSELECT 1;",
		"-- ratel:step class=huge\nSELECT 1;",
		"-- ratel:step by=robot\nSELECT 1;",
		"-- ratel:step locks=app.t\nSELECT 1;",
		"-- ratel:step locks=app.t:Huge\nSELECT 1;",
		"-- ratel:step locks=%zz:Share\nSELECT 1;",
		"-- ratel:step key=%zz\nSELECT 1;",
		"-- ratel:step pg=x\nSELECT 1;",
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

func BenchmarkParse(b *testing.B) {
	sql := render(sealedSteps("x.sql"))

	for b.Loop() {
		if _, err := migrate.Parse("x.sql", sql); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSeal(b *testing.B) {
	sql := "CREATE TABLE app.t (id int);\n-- @ratel.allow RTL-D01 reason=x\nDROP TABLE app.old;\n"

	file, err := migrate.Parse("x.sql", sql)
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		migrate.Seal(sql, file)
	}
}
