//go:build integration

package tests_test

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gopherex/ratel/migrate"
)

// migrations is an application's migrations directory, embedded as an
// application embeds its own.
//
//go:embed testdata/migrations
var migrations embed.FS

// migrateSettings keep waits short: blocked steps fail within a second.
func migrateSettings() migrate.Option {
	return migrate.WithSettings(migrate.Settings{
		LockTimeout: 20 * time.Millisecond, MaxTotalWait: time.Second,
		StatementTimeout: 5 * time.Second, LongTxThreshold: time.Hour,
		Backfill: migrate.BackfillSettings{Batch: 10, MaxReplicaLag: time.Second},
	})
}

// migrateServer is a PostgreSQL container and a pool on its admin database.
func migrateServer(t *testing.T) *pgxpool.Pool {
	t.Helper()

	image := os.Getenv("RATEL_PG_IMAGE")
	if image == "" {
		image = defaultImage
	}

	ctx, cancel := context.WithTimeout(t.Context(), containerWait)
	defer cancel()

	container, err := postgres.Run(ctx, image, testcontainers.WithWaitStrategy(
		wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(containerWait),
	))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), containerWait)
		defer cleanupCancel()

		if err := container.Terminate(cleanupCtx); err != nil {
			t.Errorf("terminate PostgreSQL: %v", err)
		}
	})

	admin, err := pgxpool.New(ctx, container.MustConnectionString(ctx))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(admin.Close)

	return admin
}

// target is a fresh database holding app.t with 25 rows, named after the
// test.
func target(ctx context.Context, t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()

	name := strings.Map(func(char rune) rune {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			return unicode.ToLower(char)
		}

		return '_'
	}, t.Name())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}

	config := admin.Config()
	config.ConnConfig.Database = name

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, `CREATE SCHEMA app;
		CREATE TABLE app.t (id int PRIMARY KEY, v int, w int);
		INSERT INTO app.t (id, v) SELECT g AS id, g AS v FROM generate_series(1, 25) g;`)
	if err != nil {
		t.Fatal(err)
	}

	return pool
}

// directory is a migrations directory of the files, with their ratel.sum.
func directory(files ...migrate.Source) fstest.MapFS {
	result := fstest.MapFS{migrate.SumFile: {Data: migrate.Sum(files)}}
	for _, file := range files {
		result[file.Name] = &fstest.MapFile{Data: []byte(file.SQL)}
	}

	return result
}

func TestMigrate(t *testing.T) {
	t.Parallel()

	admin := migrateServer(t)

	for name, check := range map[string]func(context.Context, *testing.T, *pgxpool.Pool){
		"phases and journal": checkPhases, "blocked then free": checkBlocked, "resume": checkResume,
		"busy": checkBusy, "server version": checkServerVersion, "refused directories": checkRefused,
		"embedded": checkEmbedded,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The deadline starts here: a parallel subtest waits for a free slot
			// after its parent returns, and that wait must not eat its budget.
			ctx, cancel := context.WithTimeout(t.Context(), containerWait)
			defer cancel()

			check(ctx, t, admin)
		})
	}
}

const phased = `-- ratel:step id=a1 phase=expand tx=in class=short locks=app.t:AccessExclusive pg=16
ALTER TABLE app.t ADD COLUMN x int;
-- ratel:step id=a2 phase=expand tx=own class=long locks=app.t:ShareUpdateExclusive pg=16
CREATE INDEX CONCURRENTLY t_v ON app.t (v);
-- ratel:step id=a3 phase=backfill tx=in class=backfill locks=app.t:RowExclusive pg=16 key=id
UPDATE app.t SET w = v * 2 WHERE w IS NULL;
-- ratel:step id=a4 phase=contract tx=in class=short locks=app.t:AccessExclusive pg=16
ALTER TABLE app.t DROP COLUMN x;
`

func checkPhases(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)
	files := directory(migrate.Source{Name: "1_phased.sql", SQL: phased})

	if err := migrate.Apply(ctx, pool, files, migrateSettings()); err != nil {
		t.Fatal(err)
	}

	states := func() []migrate.State {
		status, err := migrate.Status(ctx, pool, files)
		if err != nil {
			t.Fatal(err)
		}

		result := make([]migrate.State, 0, len(status))
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

	if err := migrate.Contract(ctx, pool, files, migrateSettings()); err != nil {
		t.Fatal(err)
	}

	// A second run finds everything done.
	if err := migrate.Apply(ctx, pool, files, migrateSettings()); err != nil {
		t.Fatal(err)
	}

	for _, state := range states() {
		if state != migrate.Applied {
			t.Fatalf("after contract = %v", states())
		}
	}

	changed := directory(migrate.Source{
		Name: "1_phased.sql", SQL: strings.Replace(phased, "ADD COLUMN x", "ADD COLUMN y", 1),
	})

	if err := migrate.Apply(ctx, pool, changed, migrateSettings()); !errors.Is(err, migrate.ErrChanged) {
		t.Fatalf("changed step = %v", err)
	}
}

func checkBlocked(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)
	files := directory(migrate.Source{Name: "1_add.sql", SQL: `-- ratel:step id=h1 phase=expand tx=in class=short ` +
		`by=hand locks=app.t:AccessExclusive pg=16 ops=add-column:app.t::false
ALTER TABLE app.t ADD COLUMN x int;`})

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = blocker.Rollback(context.WithoutCancel(ctx)) }()

	// The blocker is a session of the application, not of the run.
	_, err = blocker.Exec(ctx, `SET application_name = 'blocker'; LOCK TABLE app.t IN ROW EXCLUSIVE MODE`)
	if err != nil {
		t.Fatal(err)
	}

	err = migrate.Apply(ctx, pool, files, migrateSettings())
	if !errors.Is(err, migrate.ErrBlocked) || !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("blocked step = %v", err)
	}

	released := make(chan error, 1)

	go func() {
		time.Sleep(200 * time.Millisecond)

		released <- blocker.Rollback(context.WithoutCancel(ctx))
	}()

	if err := migrate.Apply(ctx, pool, files, migrateSettings()); err != nil {
		t.Fatalf("after the blocker left: %v", err)
	}

	if err := <-released; err != nil {
		t.Fatal(err)
	}
}

func checkResume(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)

	// A unique build over duplicates fails and leaves an invalid index; the
	// journal records the step failed.
	if _, err := pool.Exec(ctx, `UPDATE app.t SET v = 1 WHERE id <= 2`); err != nil {
		t.Fatal(err)
	}

	index := directory(migrate.Source{Name: "1_index.sql", SQL: `-- ratel:step id=u1 phase=expand tx=own class=long pg=16
CREATE UNIQUE INDEX CONCURRENTLY t_v_key ON app.t (v);`})

	if err := migrate.Apply(ctx, pool, index, migrateSettings()); err == nil {
		t.Fatal("build over duplicates succeeded")
	}

	var leftover bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('app.t_v_key') IS NOT NULL`).Scan(&leftover); err != nil || leftover {
		t.Fatalf("invalid index left: %v, %v", leftover, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE app.t SET v = id`); err != nil {
		t.Fatal(err)
	}

	if err := migrate.Apply(ctx, pool, index, migrateSettings()); err != nil {
		t.Fatal(err)
	}

	// A backfill resumes after its recorded key.
	fill := migrate.Source{Name: "2_fill.sql", SQL: `-- ratel:step id=f1 phase=backfill tx=in class=backfill pg=16
UPDATE app.t SET w = 1;`}

	_, err := pool.Exec(ctx, `INSERT INTO ratel.migrations_journal (step_id, file, checksum, phase, started_at, resume_key)
		VALUES ('f1', '2_fill.sql', $1, 'backfill', now(), '{"statement":0,"key":["20"]}')`,
		migrate.Checksum([]string{"UPDATE app.t SET w = 1"}))
	if err != nil {
		t.Fatal(err)
	}

	indexFile := migrate.Source{Name: "1_index.sql", SQL: string(index["1_index.sql"].Data)}
	if err := migrate.Apply(ctx, pool, directory(indexFile, fill), migrateSettings()); err != nil {
		t.Fatal(err)
	}

	var filled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.t WHERE w = 1`).Scan(&filled); err != nil || filled != 5 {
		t.Fatalf("resumed backfill filled %d rows, %v", filled, err)
	}
}

func checkBusy(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)
	files := directory(migrate.Source{Name: "1_wait.sql", SQL: `-- ratel:step id=w1 phase=expand tx=own class=long pg=16
SELECT pg_sleep(1);`})

	done := make(chan error, 1)

	go func() { done <- migrate.Apply(ctx, pool, files, migrateSettings()) }()

	time.Sleep(300 * time.Millisecond)

	if err := migrate.Apply(ctx, pool, files, migrateSettings()); !errors.Is(err, migrate.ErrBusy) {
		t.Fatalf("second run = %v", err)
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func checkServerVersion(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)
	files := directory(migrate.Source{Name: "1_new.sql", SQL: `-- ratel:step id=n1 phase=expand tx=in class=short pg=19
SELECT 1;`})

	if err := migrate.Apply(ctx, pool, files, migrateSettings()); !errors.Is(err, migrate.ErrServer) {
		t.Fatalf("step for 19 = %v", err)
	}
}

// checkRefused runs nothing from a directory that does not match its
// ratel.sum or holds an unsealed step.
func checkRefused(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)
	tampered := directory(migrate.Source{Name: "1_drop.sql", SQL: phased})
	tampered["1_drop.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE app.t;")}

	for name, tc := range map[string]struct {
		files fs.FS
		want  error
	}{
		"tampered": {tampered, migrate.ErrSum},
		"unsealed": {directory(migrate.Source{Name: "1_drop.sql", SQL: "DROP TABLE app.t;"}), migrate.ErrUnsealed},
	} {
		if err := migrate.Apply(ctx, pool, tc.files, migrateSettings()); !errors.Is(err, tc.want) {
			t.Errorf("%s: Apply() = %v, want %v", name, err, tc.want)
		}
	}

	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('app.t') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("app.t after refused runs: %v, %v", exists, err)
	}
}

// checkEmbedded applies an embedded migrations directory the way an
// application does on startup, twice, then contracts it.
func checkEmbedded(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	t.Helper()

	pool := target(ctx, t, admin)

	embedded, err := fs.Sub(migrations, "testdata/migrations")
	if err != nil {
		t.Fatal(err)
	}

	if err := migrate.Check(embedded); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := migrate.Apply(ctx, pool, embedded, migrateSettings()); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrate.Contract(ctx, pool, embedded, migrateSettings()); err != nil {
		t.Fatal(err)
	}

	var email bool
	if err := pool.QueryRow(ctx, `SELECT count(*) = 25 FROM app.t WHERE email = 'user' || id || '@example.org'`).
		Scan(&email); err != nil || !email {
		t.Fatalf("embedded migrations: %v, %v", email, err)
	}

	var dropped bool
	if err := pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'app' AND table_name = 't' AND column_name = 'w')`).Scan(&dropped); err != nil || !dropped {
		t.Fatalf("contract: %v, %v", dropped, err)
	}
}
