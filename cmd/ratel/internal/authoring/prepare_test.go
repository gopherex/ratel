package authoring_test

import (
	"testing"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func TestClassify(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	file, err := authoring.Parse(migrate.Source{Name: "1_x.sql", SQL: `CREATE INDEX CONCURRENTLY i ON app.t (a);
ALTER TABLE app.t ADD COLUMN c int;
-- ratel:step phase=backfill
UPDATE app.t SET c = 1;
-- ratel:step class=long
NOT SQL AT ALL;
-- ratel:step id=p1 phase=expand tx=in class=short pg=18
CREATE TABLE app.u (id int);`})
	if err != nil {
		t.Fatal(err)
	}

	authoring.Classify(&file, safety.NewClassifier(parser, safety.Env{}), parser)

	for index, want := range []struct {
		class migrate.Class
		tx    migrate.TxMode
		kind  safety.Kind
		lock  migrate.LockMode
	}{
		{migrate.ClassLong, migrate.OwnStatement, safety.CreateIndexConcurrently, migrate.ShareUpdateExclusive},
		{migrate.ClassShort, migrate.InTx, safety.AddColumn, migrate.AccessExclusive},
		{migrate.ClassBatched, migrate.InTx, safety.Backfill, migrate.RowExclusive},
		{migrate.ClassLong, migrate.InTx, safety.Unknown, migrate.NoLock},
	} {
		step := file.Steps[index]
		if step.Class != want.class || step.Tx != want.tx || step.Operations[0].Kind != want.kind {
			t.Errorf("step %d = %s %d %v", index, step.Class, step.Tx, step.Operations)
		}

		if want.lock != migrate.NoLock && (len(step.Locks) == 0 || step.Locks[0].Mode != want.lock) {
			t.Errorf("step %d locks = %v, want %s", index, step.Locks, want.lock)
		}
	}

	if file.Steps[4].Operations != nil {
		t.Errorf("planned step classified: %v", file.Steps[4].Operations)
	}
}

func BenchmarkClassify(b *testing.B) {
	parser := newParser(b)
	classifier := safety.NewClassifier(parser, safety.Env{})

	file, err := authoring.Parse(migrate.Source{
		Name: "1_x.sql", SQL: "CREATE INDEX CONCURRENTLY i ON app.t (a);\nALTER TABLE app.t ADD COLUMN c int;",
	})
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		authoring.Classify(&file, classifier, parser)
	}
}
