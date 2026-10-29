package migrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// lockNotAvailable is SQLSTATE 55P03, raised when lock_timeout expires.
const lockNotAvailable = "55P03"

// transactionTimeout is the first major version with transaction_timeout.
const transactionTimeout = 17

// blockers are the other sessions a step would wait for: those holding a
// lock conflicting with one it takes, and those in a transaction older than
// long_tx_threshold. Sessions of this run are not counted.
const blockers = `
SELECT a.pid, COALESCE(a.application_name, ''), COALESCE(a.state, ''),
       COALESCE(now() - a.xact_start, interval '0')::text, left(COALESCE(a.query, ''), 200)
FROM pg_catalog.pg_stat_activity a
WHERE a.pid <> pg_backend_pid() AND a.application_name IS DISTINCT FROM $3
  AND (a.xact_start < now() - make_interval(secs => $4)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_locks l
                  JOIN unnest($1::text[], $2::text[]) AS w(relation, modes)
                    ON l.relation = to_regclass(w.relation)
                  WHERE l.pid = a.pid AND l.granted AND l.locktype = 'relation'
                    AND l.mode = ANY(string_to_array(w.modes, ','))))
ORDER BY a.pid`

// session is one blocking session, as a failure reports it.
type session struct{ pid, application, state, age, query string }

func (s session) String() string {
	return fmt.Sprintf("pid %s (%s, %s, transaction %s): %s", s.pid, s.application, s.state, s.age, s.query)
}

// blocking lists the sessions a step would wait for.
func (r *runner) blocking(ctx context.Context, step *Step, checkLocks bool) ([]session, error) {
	var relations, modes []string

	if checkLocks {
		for _, lock := range step.Locks {
			relations = append(relations, lock.Relation)
			modes = append(modes, conflicting(lock.Mode))
		}
	}

	threshold := r.options.settings.LongTxThreshold.Seconds()

	rows, err := r.pool.Query(ctx, blockers, relations, modes, ApplicationName, threshold)
	if err != nil {
		return nil, fmt.Errorf("pre-flight: %w", err)
	}

	defer rows.Close()

	var result []session

	for rows.Next() {
		var (
			pid   int32
			found session
		)

		if err := rows.Scan(&pid, &found.application, &found.state, &found.age, &found.query); err != nil {
			return nil, fmt.Errorf("pre-flight: %w", err)
		}

		found.pid = strconv.Itoa(int(pid))
		result = append(result, found)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pre-flight: %w", err)
	}

	return result, nil
}

// conflicting is the pg_locks names of the modes a mode conflicts with.
func conflicting(mode LockMode) string {
	var names []string

	for other := AccessShare; other <= AccessExclusive; other++ {
		if mode.Conflicts(other) {
			names = append(names, other.String()+"Lock")
		}
	}

	return strings.Join(names, ",")
}

// clear waits, without requesting any lock, until no session would block
// the step, pausing on the retry schedule until the deadline.
func (r *runner) clear(ctx context.Context, step *Step, checkLocks bool, deadline time.Time) error {
	attempts := schedule(r.options.settings.LockTimeout)

	for index := 0; ; index++ {
		found, err := r.blocking(ctx, step, checkLocks)
		if err != nil || len(found) == 0 {
			return err
		}

		if time.Now().After(deadline) {
			return blocked(found)
		}

		if err := r.pause(ctx, r.jitter(attempts[min(index, len(attempts)-1)].pause)); err != nil {
			return err
		}
	}
}

func blocked(found []session) error {
	lines := make([]string, 0, len(found))
	for _, item := range found {
		lines = append(lines, item.String())
	}

	return fmt.Errorf("%w by: %s", ErrBlocked, strings.Join(lines, "; "))
}

// short runs a step that takes blocking locks briefly: after the pre-flight
// it tries under a growing lock_timeout, pausing after each timeout, until
// max_total_wait. The journal records it done in the step's transaction.
func (r *runner) short(ctx context.Context, step *Step) error {
	deadline := time.Now().Add(r.options.settings.MaxTotalWait)
	attempts := schedule(r.options.settings.LockTimeout)

	for index := 0; ; index++ {
		if err := r.clear(ctx, step, true, deadline); err != nil {
			return err
		}

		attempt := attempts[min(index, len(attempts)-1)]

		err := r.attempt(ctx, step, attempt.timeout)

		var failure *pgconn.PgError
		if !errors.As(err, &failure) || failure.Code != lockNotAvailable {
			return err
		}

		if time.Now().After(deadline) || index == len(attempts)-1 {
			found, err := r.blocking(ctx, step, true)

			return errors.Join(blocked(found), err)
		}

		if err := r.pause(ctx, r.jitter(attempt.pause)); err != nil {
			return err
		}
	}
}

// attempt runs a short step's statements in one transaction under the
// guards, and records it done there.
func (r *runner) attempt(ctx context.Context, step *Step, lockTimeout time.Duration) error {
	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	defer connection.Release()

	major, err := r.major(ctx)
	if err != nil {
		return err
	}

	return inTransaction(ctx, connection.Conn(), func(transaction pgx.Tx) error {
		settings := []string{
			"SET LOCAL lock_timeout = " + milliseconds(lockTimeout),
			"SET LOCAL statement_timeout = " + milliseconds(r.options.settings.StatementTimeout),
			"SET LOCAL TimeZone = 'UTC'",
		}

		if major >= transactionTimeout {
			total := lockTimeout + r.options.settings.StatementTimeout*time.Duration(len(step.Statements))
			settings = append(settings, "SET LOCAL transaction_timeout = "+milliseconds(total))
		}

		for _, statement := range append(settings, step.Statements...) {
			if _, err := transaction.Exec(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", firstLine(statement), err)
			}
		}

		_, err := transaction.Exec(ctx, `UPDATE ratel.migrations_journal SET outcome = 'done', finished_at = now()
			WHERE step_id = $1`, step.ID)
		if err != nil {
			return fmt.Errorf("journal: %w", err)
		}

		return nil
	})
}

func milliseconds(duration time.Duration) string {
	return fmt.Sprintf("'%dms'", duration.Milliseconds())
}

func firstLine(statement string) string {
	line, _, _ := strings.Cut(statement, "\n")

	return line
}

// long runs a step that cannot run in a transaction, statement by
// statement without lock_timeout, after the pre-flight on old transactions.
// A resumed step first checks what its interrupted run left.
func (r *runner) long(ctx context.Context, step *Step, resumed bool) error {
	if err := r.clear(ctx, step, false, time.Now().Add(r.options.settings.MaxTotalWait)); err != nil {
		return err
	}

	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	defer connection.Release()

	for _, setting := range []string{"SET lock_timeout = 0", "SET statement_timeout = 0", "SET TimeZone = 'UTC'"} {
		if _, err := connection.Exec(ctx, setting); err != nil {
			return fmt.Errorf("%s: %w", setting, err)
		}
	}

	defer func() { _, _ = connection.Exec(context.WithoutCancel(ctx), "RESET ALL") }()

	for _, statement := range step.Statements {
		run := statement

		if resumed {
			if run, err = r.resume(ctx, statement); err != nil {
				return err
			}
		}

		if run == "" {
			continue
		}

		if _, err := connection.Exec(ctx, run); err != nil {
			return errors.Join(fmt.Errorf("%s: %w", firstLine(run), err), r.leftover(ctx, run))
		}
	}

	return nil
}
