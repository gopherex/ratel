package authoring_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func planned() plan.Plan {
	index := safety.Operation{Kind: safety.CreateIndexConcurrently, Relation: `app."Odd, table"`}
	fill := safety.Operation{Kind: safety.Backfill, Relation: "app.t", Rewrite: true}

	return plan.Plan{Steps: []plan.Step{
		{
			Phase: migrate.PhaseExpand, Class: migrate.ClassLong,
			Statements: []plan.Statement{{
				SQL:        `CREATE INDEX CONCURRENTLY i ON app."Odd, table" (a);`,
				Operations: []safety.Operation{index},
			}},
			Safety: safety.Safety{
				Tx:    migrate.OwnStatement,
				Locks: []migrate.RelLock{{Relation: `app."Odd, table"`, Mode: migrate.ShareUpdateExclusive}},
			},
			Allow: []string{"RTL-C01", "RTL-L02"},
		},
		{
			Phase: migrate.PhaseBackfill, Class: migrate.ClassBatched,
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

	name := "20261029010203_add_index.sql"
	written := string(authoring.Write(name, planned(), 18))

	file, err := authoring.Parse(migrate.Source{Name: name, SQL: written})
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(file.Steps))

	for index := range file.Steps {
		step := &file.Steps[index]
		got = append(got, strings.Join([]string{
			step.ID, string(step.Phase), string(step.Class), strings.Join(step.Allow, ","), strings.Join(step.Key, ","),
		}, "|"))
	}

	want := []string{
		migrate.StepID(name, []string{`CREATE INDEX CONCURRENTLY i ON app."Odd, table" (a)`}) +
			"|expand|long|RTL-C01,RTL-L02|",
		migrate.StepID(name, []string{"UPDATE app.t SET f = 'x;y' WHERE f IS NULL"}) + "|backfill|backfill||a,b",
	}
	if difference := cmp.Diff(want, got); difference != "" {
		t.Errorf("Parse(Write()) (-want +got):\n%s\n%s", difference, written)
	}

	wantOperations := [][]safety.Operation{
		{{Kind: safety.CreateIndexConcurrently, Relation: `app."Odd, table"`}},
		{{Kind: safety.Backfill, Relation: "app.t", Rewrite: true}},
	}

	for index := range file.Steps {
		step := &file.Steps[index]
		if !step.Planned() || !cmp.Equal(step.Operations, wantOperations[index]) {
			t.Errorf("step %d = planned %v, operations %v", index, step.Planned(), step.Operations)
		}
	}

	if file.Steps[0].Tx != migrate.OwnStatement || file.Steps[0].Locks[0].Mode != migrate.ShareUpdateExclusive {
		t.Errorf("first step = %+v", file.Steps[0].Step)
	}
}

func TestSeal(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	sql := "-- add a column\nALTER TABLE app.t ADD COLUMN c int;\n-- ratel:step phase=backfill\nUPDATE app.t SET c = 1;\n"

	file, err := authoring.Parse(migrate.Source{Name: "1_hand.sql", SQL: sql})
	if err != nil {
		t.Fatal(err)
	}

	authoring.Classify(&file, safety.NewClassifier(parser, safety.Env{}), parser)

	sealed := authoring.Seal(&file, 18)
	if !strings.HasPrefix(sealed, "-- add a column\n-- ratel:step id=") ||
		!strings.Contains(sealed, "by=hand locks=app.t:AccessExclusive pg=18 ops=add-column:app.t::false\n") {
		t.Fatalf("Seal() =\n%s", sealed)
	}

	again, err := authoring.Parse(migrate.Source{Name: "1_hand.sql", SQL: sealed})
	if err != nil {
		t.Fatal(err)
	}

	for index := range again.Steps {
		step := &again.Steps[index]
		if !step.Sealed || !step.Hand || step.Planned() || step.ID != file.Steps[index].ID ||
			!cmp.Equal(step.Operations, file.Steps[index].Operations) {
			t.Errorf("sealed step %d = %+v", index, step)
		}
	}

	if again.Steps[1].Class != migrate.ClassBatched || again.Steps[1].Phase != migrate.PhaseBackfill {
		t.Errorf("backfill step = %+v", again.Steps[1].Step)
	}
}

func TestOps(t *testing.T) {
	t.Parallel()

	operations := []safety.Operation{
		{Kind: safety.AddForeignKey, Relation: `app."a,b"`, Other: "app.c"},
		{Kind: safety.AlterColumnType, Relation: "app.t", Rewrite: true},
	}

	decoded, err := authoring.DecodeOps(authoring.EncodeOps(operations))
	if err != nil || !cmp.Equal(decoded, operations) {
		t.Errorf("DecodeOps(EncodeOps()) = %v, %v", decoded, err)
	}

	if empty, err := authoring.DecodeOps(""); empty != nil || err != nil {
		t.Errorf("DecodeOps(\"\") = %v, %v", empty, err)
	}

	for _, bad := range []string{"a:b", "a:%zz::false", "a::%zz:false"} {
		if _, err := authoring.DecodeOps(bad); !errors.Is(err, migrate.ErrHeader) {
			t.Errorf("DecodeOps(%q) = %v", bad, err)
		}
	}

	if _, err := authoring.Parse(migrate.Source{Name: "x.sql", SQL: "-- ratel:step ops=a:b\nSELECT 1;"}); err == nil {
		t.Error("Parse accepted a bad ops field")
	}
}

func BenchmarkWriteParse(b *testing.B) {
	planned := planned()

	for b.Loop() {
		source := migrate.Source{Name: "x.sql", SQL: string(authoring.Write("x.sql", planned, 18))}
		if _, err := authoring.Parse(source); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSeal(b *testing.B) {
	parser := newParser(b)

	file, err := authoring.Parse(migrate.Source{Name: "1_hand.sql", SQL: "ALTER TABLE app.t ADD COLUMN c int;\n"})
	if err != nil {
		b.Fatal(err)
	}

	authoring.Classify(&file, safety.NewClassifier(parser, safety.Env{}), parser)

	for b.Loop() {
		authoring.Seal(&file, 18)
	}
}
