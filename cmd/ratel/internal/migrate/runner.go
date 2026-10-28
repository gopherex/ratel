package migrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// LatestMajor is the newest PostgreSQL major version ratel supports.
const LatestMajor = 18

// outcomeDone is the journal outcome of a finished step.
const outcomeDone = "done"

// jitterSides is the jitter window: a quarter on each side.
const jitterSides = 2

// ApplicationName is the application_name every step runs under.
const ApplicationName = "ratel-migrate"

// Runner errors.
var (
	// ErrBusy is another run holding the advisory lock of the database.
	ErrBusy = errors.New("another ratel migrate run holds the lock of this database")
	// ErrServer is a server whose major version a step was not planned for.
	ErrServer = errors.New("server version does not match the plan")
	// ErrBlocked is a step that could not take its locks within max_total_wait.
	ErrBlocked = errors.New("step blocked")
	// ErrChanged is an applied step whose SQL changed since.
	ErrChanged = errors.New("applied step changed")
)

// journal is the table recording each applied step.
const journal = `
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

// Runner applies migration steps to a target database under the guards of
// docs/migrations.md.
type Runner struct {
	pool     *pgxpool.Pool
	settings config.Migrate
	report   io.Writer
	// pause waits between attempts; jitter spreads a pause.
	pause  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// Connect opens the target pool every step runs on, its sessions named
// ApplicationName.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	settings, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}

	settings.ConnConfig.RuntimeParams["application_name"] = ApplicationName

	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}

	return pool, nil
}

// NewRunner runs steps on pool, whose connections carry ApplicationName,
// reporting progress to report.
func NewRunner(pool *pgxpool.Pool, settings config.Migrate, report io.Writer) *Runner {
	return &Runner{pool: pool, settings: settings, report: report, pause: sleep, jitter: spread}
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

// Apply runs the pending steps of the phases, file by file in name order,
// holding the run's advisory lock.
func (r *Runner) Apply(ctx context.Context, files []File, phases ...plan.Phase) error {
	lock, err := r.lock(ctx)
	if err != nil {
		return err
	}

	defer lock.Release()

	if _, err := r.pool.Exec(ctx, journal); err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	major, err := r.major(ctx)
	if err != nil {
		return err
	}

	done, err := r.journal(ctx)
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

func (r *Runner) journal(ctx context.Context) (map[string]entry, error) {
	rows, err := r.pool.Query(ctx, `SELECT step_id, checksum, COALESCE(outcome, ''), finished_at IS NOT NULL,
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
func (r *Runner) run(ctx context.Context, file string, step *Step, major int, recorded entry) error {
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

	fmt.Fprintf(r.report, "%s %s %s %s\n", step.Phase, step.Class, file, step.ID)

	if recorded.checksum == "" {
		if err := r.start(ctx, file, step, checksum); err != nil {
			return err
		}
	}

	var err error

	switch {
	case step.Phase == plan.Backfill:
		err = r.backfill(ctx, step, recorded.resume)
	case step.Tx == safety.OwnStatement:
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

func (r *Runner) start(ctx context.Context, file string, step *Step, checksum string) error {
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
func (r *Runner) finish(ctx context.Context, stepID string, failure error) error {
	outcome := outcomeDone
	if failure != nil {
		outcome = "failed"
	}

	_, err := r.pool.Exec(context.WithoutCancel(ctx), `UPDATE ratel.migrations_journal
		SET outcome = $2, finished_at = CASE WHEN $2 = 'done' THEN now() END WHERE step_id = $1`, stepID, outcome)

	return errors.Join(failure, err)
}

func (r *Runner) major(ctx context.Context) (int, error) {
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
func (r *Runner) lock(ctx context.Context) (*pgxpool.Conn, error) {
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
