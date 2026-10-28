//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// runnerSettings keep waits short: blocked steps fail within a second.
func runnerSettings() config.Migrate {
	return config.Migrate{
		LockTimeout: 20 * time.Millisecond, RetrySchedule: "gitlab", MaxTotalWait: time.Second,
		StatementTimeout: 5 * time.Second, LongTxThreshold: time.Hour,
		Backfill: config.Backfill{Batch: 10, MaxReplicaLag: time.Second},
	}
}

// target is a fresh database holding app.t with 25 rows, and a runner on it.
func target(ctx context.Context, t *testing.T, server *devdb.Server) (*pgxpool.Pool, *migrate.Runner) {
	t.Helper()

	database, err := server.Database(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = database.Drop(context.WithoutCancel(ctx)) })

	_, err = database.Pool.Exec(ctx, `CREATE SCHEMA app;
		CREATE TABLE app.t (id int PRIMARY KEY, v int, w int);
		INSERT INTO app.t SELECT g, g, NULL FROM generate_series(1, 25) g;`)
	if err != nil {
		t.Fatal(err)
	}

	pool, err := migrate.Connect(ctx, database.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	return pool, migrate.NewRunner(pool, runnerSettings(), &bytes.Buffer{})
}

func parsed(t *testing.T, name, sql string) migrate.File {
	t.Helper()

	file, err := migrate.Parse(name, sql)
	if err != nil {
		t.Fatal(err)
	}

	migrate.Classify(&file, safety.NewClassifier(newParser(t), safety.Env{}), newParser(t))

	return file
}

func TestRunner(t *testing.T) {
	t.Parallel()

	pool := postgresPool(t, 18)

	ctx, cancel := context.WithTimeout(t.Context(), 2*startupTimeout)
	defer cancel()

	server, err := devdb.Start(ctx, config.Dev{URL: pool.Config().ConnString()})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close(context.WithoutCancel(ctx)) }()

	for name, check := range map[string]func(context.Context, *testing.T, *devdb.Server){
		"phases and journal": checkPhases, "blocked then free": checkBlocked, "resume": checkResume,
		"busy": checkBusy, "server version": checkServerVersion,
	} {
		t.Run(name, func(t *testing.T) { check(ctx, t, server) })
	}
}

const phased = `-- ratel:step id=a1 phase=expand tx=in class=short pg=18 ops=add-column:app.t::false
ALTER TABLE app.t ADD COLUMN x int;
-- ratel:step id=a2 phase=expand tx=own class=long pg=18 ops=create-index-concurrently:app.t::false
CREATE INDEX CONCURRENTLY t_v ON app.t (v);
-- ratel:step id=a3 phase=backfill tx=in class=backfill pg=18 ops=backfill:app.t::false key=id
UPDATE app.t SET w = v * 2 WHERE w IS NULL;
-- ratel:step id=a4 phase=contract tx=in class=short pg=18 ops=drop-column:app.t::false
ALTER TABLE app.t DROP COLUMN x;
`

func checkPhases(ctx context.Context, t *testing.T, server *devdb.Server) {
	pool, runner := target(ctx, t, server)
	files := []migrate.File{parsed(t, "1_phased.sql", phased)}

	if err := runner.Apply(ctx, files, plan.Expand, plan.Backfill); err != nil {
		t.Fatal(err)
	}

	states := func() []migrate.State {
		status, err := runner.Status(ctx, files)
		if err != nil {
			t.Fatal(err)
		}

		var result []migrate.State
		for _, step := range status {
			result = append(result, step.State)
		}

		return result
	}

	if got := states(); got[2] != migrate.Applied || got[3] != migrate.Pending {
		t.Fatalf("after apply = %v", got)
	}

	var filled bool
	if err := pool.QueryRow(ctx, `SELECT bool_and(w = v * 2) FROM app.t`).Scan(&filled); err != nil || !filled {
		t.Fatalf("backfill: %v, %v", filled, err)
	}

	if err := runner.Apply(ctx, files, plan.Contract); err != nil {
		t.Fatal(err)
	}

	// A second run finds everything done.
	if err := runner.Apply(ctx, files, plan.Expand, plan.Backfill, plan.Contract); err != nil {
		t.Fatal(err)
	}

	for _, state := range states() {
		if state != migrate.Applied {
			t.Fatalf("after contract = %v", states())
		}
	}

	changed := []migrate.File{parsed(t, "1_phased.sql", phased+"\n")}
	changed[0].Steps[0].Statements[0] = "ALTER TABLE app.t ADD COLUMN y int"

	if err := runner.Apply(ctx, changed, plan.Expand); !errors.Is(err, migrate.ErrChanged) {
		t.Fatalf("changed step = %v", err)
	}
}

func checkBlocked(ctx context.Context, t *testing.T, server *devdb.Server) {
	pool, runner := target(ctx, t, server)
	files := []migrate.File{parsed(t, "1_add.sql", "ALTER TABLE app.t ADD COLUMN x int;")}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = blocker.Rollback(context.WithoutCancel(ctx)) }()

	// The blocker is not a ratel session.
	if _, err := blocker.Exec(ctx, `SET application_name = 'blocker'; LOCK TABLE app.t IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	err = runner.Apply(ctx, files, plan.Expand)
	if !errors.Is(err, migrate.ErrBlocked) || !bytes.Contains([]byte(err.Error()), []byte("blocker")) {
		t.Fatalf("blocked step = %v", err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = blocker.Rollback(context.WithoutCancel(ctx))
	}()

	if err := runner.Apply(ctx, files, plan.Expand); err != nil {
		t.Fatalf("after the blocker left: %v", err)
	}
}

func checkResume(ctx context.Context, t *testing.T, server *devdb.Server) {
	pool, runner := target(ctx, t, server)

	// A unique build over duplicates fails and leaves an invalid index; the
	// journal records the step failed.
	if _, err := pool.Exec(ctx, `UPDATE app.t SET v = 1 WHERE id <= 2`); err != nil {
		t.Fatal(err)
	}

	index := []migrate.File{parsed(t, "1_index.sql", `-- ratel:step id=u1 phase=expand tx=own class=long pg=18
CREATE UNIQUE INDEX CONCURRENTLY t_v_key ON app.t (v);`)}

	if err := runner.Apply(ctx, index, plan.Expand); err == nil {
		t.Fatal("build over duplicates succeeded")
	}

	var leftover bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('app.t_v_key') IS NOT NULL`).Scan(&leftover); err != nil || leftover {
		t.Fatalf("invalid index left: %v, %v", leftover, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE app.t SET v = id`); err != nil {
		t.Fatal(err)
	}

	if err := runner.Apply(ctx, index, plan.Expand); err != nil {
		t.Fatal(err)
	}

	// A backfill resumes after its recorded key.
	fill := []migrate.File{parsed(t, "2_fill.sql", `-- ratel:step id=f1 phase=backfill tx=in class=backfill pg=18
UPDATE app.t SET w = 1;`)}

	_, err := pool.Exec(ctx, `INSERT INTO ratel.migrations_journal (step_id, file, checksum, phase, started_at, resume_key)
		VALUES ('f1', '2_fill.sql', $1, 'backfill', now(), '{"statement":0,"key":["20"]}')`,
		migrate.Checksum(fill[0].Steps[0].Statements))
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Apply(ctx, fill, plan.Backfill); err != nil {
		t.Fatal(err)
	}

	var filled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.t WHERE w = 1`).Scan(&filled); err != nil || filled != 5 {
		t.Fatalf("resumed backfill filled %d rows, %v", filled, err)
	}
}

func checkBusy(ctx context.Context, t *testing.T, server *devdb.Server) {
	pool, runner := target(ctx, t, server)

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer holder.Release()

	first := migrate.NewRunner(pool, runnerSettings(), &bytes.Buffer{})
	files := []migrate.File{parsed(t, "1_wait.sql", `-- ratel:step id=w1 phase=expand tx=own class=long pg=18
SELECT pg_sleep(1);`)}

	done := make(chan error, 1)

	go func() { done <- first.Apply(ctx, files, plan.Expand) }()

	time.Sleep(300 * time.Millisecond)

	if err := runner.Apply(ctx, files, plan.Expand); !errors.Is(err, migrate.ErrBusy) {
		t.Fatalf("second run = %v", err)
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func checkServerVersion(ctx context.Context, t *testing.T, server *devdb.Server) {
	_, runner := target(ctx, t, server)
	files := []migrate.File{parsed(t, "1_new.sql", `-- ratel:step id=n1 phase=expand tx=in class=short pg=19
SELECT 1;`)}

	if err := runner.Apply(ctx, files, plan.Expand); !errors.Is(err, migrate.ErrServer) {
		t.Fatalf("step for 19 on 18 = %v", err)
	}
}
