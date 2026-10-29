package migrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LatestMajor is the newest PostgreSQL major version ratel supports.
const LatestMajor = 18

// outcomeDone is the journal outcome of a finished step.
const outcomeDone = "done"

// jitterSides is the jitter window: a quarter on each side.
const jitterSides = 2

// ApplicationName is the application_name every step runs under.
const ApplicationName = "ratel-migrate"

// Run errors.
var (
	// ErrBusy is another run holding the advisory lock of the database.
	ErrBusy = errors.New("another ratel migrate run holds the lock of this database")
	// ErrServer is a server whose major version a step was not planned for.
	ErrServer = errors.New("server version does not match the plan")
	// ErrBlocked is a step that could not take its locks within MaxTotalWait.
	ErrBlocked = errors.New("step blocked")
	// ErrChanged is an applied step whose SQL changed since.
	ErrChanged = errors.New("applied step changed")
	// ErrUnsealed is a hand-written step without its header; ratel migrate
	// hash writes it.
	ErrUnsealed = errors.New("hand-written step is not sealed; run ratel migrate hash")
	// ErrSettings is a non-positive timeout, batch size or replica lag.
	ErrSettings = errors.New("invalid migrate settings")
)

// journalTable is the table recording each applied step.
const journalTable = `
CREATE SCHEMA IF NOT EXISTS ratel;
CREATE TABLE IF NOT EXISTS ratel.migrations_journal (
	step_id     text PRIMARY KEY,
	file        text NOT NULL,
	checksum    text NOT NULL,
	phase       text NOT NULL CHECK (phase IN ('expand', 'backfill', 'contract')),
	started_at  timestamptz NOT NULL,
	finished_at timestamptz,
	outcome     text CHECK (outcome IN ('done', 'failed')),
	resume_key  text
)`

// Apply runs the pending steps of the expand and backfill phases of the
// migrations, the directory ratel migrate plan writes with its ratel.sum,
// file by file in name order. Deploy code that works with both schemas
// before it, and run Contract once no code needs what contract removes.
func Apply(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS, options ...Option) error {
	return run(ctx, pool, migrations, configure(options), PhaseExpand, PhaseBackfill)
}

// Contract runs the pending steps of the contract phase.
func Contract(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS, options ...Option) error {
	return run(ctx, pool, migrations, configure(options), PhaseContract)
}

// runner applies steps under the guards of docs/migrations.md.
type runner struct {
	pool    *pgxpool.Pool
	options options
	// pause waits between attempts; jitter spreads a pause.
	pause  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// run applies the phases on a pool of its own, built from the caller's
// configuration with every session named ApplicationName, so the guards
// tell the run's sessions from the ones it waits for.
func run(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS, settings options, phases ...Phase) error {
	if err := settings.settings.validate(); err != nil {
		return err
	}

	files, err := load(migrations)
	if err != nil {
		return err
	}

	if err := sealed(files); err != nil {
		return err
	}

	config := pool.Config()
	config.ConnConfig.RuntimeParams["application_name"] = ApplicationName

	own, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	defer own.Close()

	return (&runner{pool: own, options: settings, pause: sleep, jitter: spread}).apply(ctx, files, phases)
}

func (s Settings) validate() error {
	if s.LockTimeout <= 0 || s.MaxTotalWait <= 0 || s.StatementTimeout <= 0 || s.LongTxThreshold <= 0 ||
		s.Backfill.Batch <= 0 || s.Backfill.Pause < 0 || s.Backfill.MaxReplicaLag <= 0 {
		return ErrSettings
	}

	return nil
}

// Check verifies the migrations as Apply does before it connects: the
// files match ratel.sum, every header reads, and every step is sealed.
func Check(migrations fs.FS) error {
	files, err := load(migrations)
	if err != nil {
		return err
	}

	return sealed(files)
}

// load reads, verifies and parses the migrations.
func load(migrations fs.FS) ([]File, error) {
	sources, err := Read(migrations)
	if err != nil {
		return nil, err
	}

	recorded, err := fs.ReadFile(migrations, SumFile)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}

	if err := Verify(sources, recorded); err != nil {
		return nil, err
	}

	files := make([]File, 0, len(sources))

	for _, source := range sources {
		file, err := Parse(source.Name, source.SQL)
		if err != nil {
			return nil, err
		}

		files = append(files, file)
	}

	return files, nil
}

// sealed refuses steps without the header the runner reads.
func sealed(files []File) error {
	for _, file := range files {
		for index := range file.Steps {
			if !file.Steps[index].Sealed {
				return fmt.Errorf("%s step %s: %w", file.Name, file.Steps[index].ID, ErrUnsealed)
			}
		}
	}

	return nil
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// jitterParts splits a pause: the jitter is one part of it either way.
const jitterParts = 4

// spread is a pause within a quarter of its length either way.
func spread(duration time.Duration) time.Duration {
	quarter := duration / jitterParts
	if quarter <= 0 {
		return duration
	}

	offset, err := rand.Int(rand.Reader, big.NewInt(int64(jitterSides*quarter)))
	if err != nil {
		return duration
	}

	return duration - quarter + time.Duration(offset.Int64())
}

// apply runs the pending steps of the phases, holding the run's advisory
// lock.
func (r *runner) apply(ctx context.Context, files []File, phases []Phase) error {
	lock, err := r.lock(ctx)
	if err != nil {
		return err
	}

	defer lock.Release()

	if _, err := r.pool.Exec(ctx, journalTable); err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	major, err := r.major(ctx)
	if err != nil {
		return err
	}

	done, err := journal(ctx, r.pool)
	if err != nil {
		return err
	}

	for _, file := range files {
		for index := range file.Steps {
			step := &file.Steps[index]
			if !slices.Contains(phases, step.Phase) {
				continue
			}

			if err := r.run(ctx, file.Name, step, major, done[step.ID]); err != nil {
				return fmt.Errorf("%s step %s: %w", file.Name, step.ID, err)
			}
		}
	}

	return nil
}

// entry is a journal row.
type entry struct {
	checksum, outcome, resume string
	finished                  bool
}

func journal(ctx context.Context, pool *pgxpool.Pool) (map[string]entry, error) {
	rows, err := pool.Query(ctx, `SELECT step_id, checksum, COALESCE(outcome, ''), finished_at IS NOT NULL,
		COALESCE(resume_key, '') FROM ratel.migrations_journal`)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}

	defer rows.Close()

	result := make(map[string]entry)

	for rows.Next() {
		var (
			stepID string
			row    entry
		)

		if err := rows.Scan(&stepID, &row.checksum, &row.outcome, &row.finished, &row.resume); err != nil {
			return nil, fmt.Errorf("journal: %w", err)
		}

		result[stepID] = row
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}

	return result, nil
}

// run applies one step unless the journal records it done.
func (r *runner) run(ctx context.Context, file string, step *Step, major int, recorded entry) error {
	checksum := Checksum(step.Statements)

	switch {
	case recorded.outcome == outcomeDone && recorded.checksum != checksum:
		return ErrChanged
	case recorded.outcome == outcomeDone:
		return nil
	case step.Major != 0 && (major < step.Major || major > LatestMajor):
		return fmt.Errorf("%w: planned for %d, server is %d", ErrServer, step.Major, major)
	default:
	}

	r.options.logger.InfoContext(ctx, "migration step",
		"phase", step.Phase, "class", step.Class, "file", file, "step", step.ID)

	return r.step(ctx, file, step, checksum, recorded)
}

func (r *runner) step(ctx context.Context, file string, step *Step, checksum string, recorded entry) error {
	if recorded.checksum == "" {
		if err := r.start(ctx, file, step, checksum); err != nil {
			return err
		}
	}

	var err error

	switch {
	case step.Phase == PhaseBackfill:
		err = r.backfill(ctx, step, recorded.resume)
	case step.Tx == OwnStatement:
		err = r.long(ctx, step, recorded.checksum != "")
	default:
		err = r.short(ctx, step)
	}

	return r.finish(ctx, step.ID, err)
}

// Checksum is the hash of a step's statements the journal records.
func Checksum(statements []string) string {
	sum := sha256.Sum256([]byte(strings.Join(statements, ";\n")))

	return hex.EncodeToString(sum[:])
}

func (r *runner) start(ctx context.Context, file string, step *Step, checksum string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO ratel.migrations_journal (step_id, file, checksum, phase, started_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (step_id) DO UPDATE SET checksum = EXCLUDED.checksum, started_at = now(), outcome = NULL`,
		step.ID, file, checksum, string(step.Phase))
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	return nil
}

// finish records the outcome; a failure is recorded without the step's
// context, which may be what was canceled.
func (r *runner) finish(ctx context.Context, stepID string, failure error) error {
	outcome := outcomeDone
	if failure != nil {
		outcome = "failed"
	}

	_, err := r.pool.Exec(context.WithoutCancel(ctx), `UPDATE ratel.migrations_journal
		SET outcome = $2, finished_at = CASE WHEN $2 = 'done' THEN now() END WHERE step_id = $1`, stepID, outcome)

	return errors.Join(failure, err)
}

func (r *runner) major(ctx context.Context) (int, error) {
	var major int

	err := r.pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&major)
	if err != nil {
		return 0, fmt.Errorf("server version: %w", err)
	}

	return major, nil
}

// lockKey is the 64-bit FNV-1a hash of "ratel.migrate" and the database
// name, as the bit string PostgreSQL casts to the bigint key.
func lockKey(database string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("ratel.migrate" + database))

	return fmt.Sprintf("%064b", hash.Sum64())
}

// lock takes the run's session advisory lock on a dedicated connection.
func (r *runner) lock(ctx context.Context) (*pgxpool.Conn, error) {
	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	var taken bool

	err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bit(64)::bigint)`,
		lockKey(connection.Conn().Config().Database)).Scan(&taken)
	if err != nil || !taken {
		connection.Release()

		return nil, errors.Join(ErrBusy, err)
	}

	return connection, nil
}

// inTransaction runs fn in a transaction and commits it.
func inTransaction(ctx context.Context, connection *pgx.Conn, body func(pgx.Tx) error) error {
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	if err := body(transaction); err != nil {
		return errors.Join(err, transaction.Rollback(context.WithoutCancel(ctx)))
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}
