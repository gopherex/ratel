package migrate_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func TestClassify(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	file, err := migrate.Parse("1_x.sql", `CREATE INDEX CONCURRENTLY i ON app.t (a);
ALTER TABLE app.t ADD COLUMN c int;
-- ratel:step phase=backfill
UPDATE app.t SET c = 1;
-- ratel:step class=long
NOT SQL AT ALL;
-- ratel:step id=p1 phase=expand tx=in class=short pg=18
CREATE TABLE app.u (id int);`)
	if err != nil {
		t.Fatal(err)
	}

	migrate.Classify(&file, safety.NewClassifier(parser, safety.Env{}), parser)

	for index, want := range []struct {
		class plan.Class
		tx    safety.TxMode
		kind  safety.Kind
	}{
		{plan.Long, safety.OwnStatement, safety.CreateIndexConcurrently},
		{plan.Short, safety.InTx, safety.AddColumn},
		{plan.Batched, safety.InTx, safety.Backfill},
		{plan.Long, safety.InTx, safety.Unknown},
	} {
		step := file.Steps[index]
		if step.Class != want.class || step.Tx != want.tx || step.Operations[0].Kind != want.kind {
			t.Errorf("step %d = %s %d %v", index, step.Class, step.Tx, step.Operations)
		}
	}

	if file.Steps[4].Operations != nil {
		t.Errorf("planned step classified: %v", file.Steps[4].Operations)
	}
}

func BenchmarkClassify(b *testing.B) {
	parser := newParser(b)
	classifier := safety.NewClassifier(parser, safety.Env{})

	file, err := migrate.Parse("1_x.sql", "CREATE INDEX CONCURRENTLY i ON app.t (a);\nALTER TABLE app.t ADD COLUMN c int;")
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		migrate.Classify(&file, classifier, parser)
	}
}
