package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// concurrentIndex is the "schema.index" a CREATE INDEX CONCURRENTLY builds.
func concurrentIndex(statement string) (string, bool) {
	upper := strings.ToUpper(statement)
	if !strings.HasPrefix(upper, "CREATE ") || !strings.Contains(upper, " INDEX CONCURRENTLY ") {
		return "", false
	}

	_, rest, _ := strings.Cut(statement, " CONCURRENTLY ")
	rest = strings.TrimPrefix(rest, "IF NOT EXISTS ")
	name, rest, _ := strings.Cut(rest, " ON ")
	table, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(rest, "ONLY "), " "), " ")

	if strings.Contains(name, ".") {
		return name, true
	}

	schema := "public"
	if dot := strings.LastIndex(table, "."); dot > 0 {
		schema = table[:dot]
	}

	return schema + "." + name, true
}

// pair is the two relation names a statement acts on.
type pair struct{ first, second string }

// validated is the table and constraint of an ALTER TABLE ... VALIDATE
// CONSTRAINT.
func validated(statement string) (pair, bool) {
	head, name, ok := strings.Cut(statement, " VALIDATE CONSTRAINT ")
	if !ok || !strings.HasPrefix(strings.ToUpper(head), "ALTER TABLE ") {
		return pair{}, false
	}

	return pair{strings.TrimSpace(head[len("ALTER TABLE "):]), strings.TrimSpace(name)}, true
}

// detached is the parent and partition of a DETACH PARTITION ... CONCURRENTLY.
func detached(statement string) (pair, bool) {
	head, partition, ok := strings.Cut(statement, " DETACH PARTITION ")
	if !ok || !strings.HasSuffix(partition, " CONCURRENTLY") {
		return pair{}, false
	}

	return pair{strings.TrimSpace(head[len("ALTER TABLE "):]), strings.TrimSuffix(partition, " CONCURRENTLY")}, true
}

// resume is what to run of a statement an interrupted run may have left
// half done: nothing when it took effect, its cleanup and itself, or the
// statement that completes it.
func (r *Runner) resume(ctx context.Context, statement string) (string, error) {
	if index, ok := concurrentIndex(statement); ok {
		return r.resumeIndex(ctx, index, statement)
	}

	if constraint, ok := validated(statement); ok {
		var done bool

		err := r.pool.QueryRow(ctx, `SELECT COALESCE(bool_or(convalidated), false) FROM pg_catalog.pg_constraint
			WHERE conrelid = to_regclass($1) AND quote_ident(conname) = $2`, constraint.first, constraint.second).
			Scan(&done)
		if err != nil {
			return "", fmt.Errorf("resume: %w", err)
		}

		if done {
			return "", nil
		}

		return statement, nil
	}

	if partition, ok := detached(statement); ok {
		return r.resumeDetach(ctx, partition, statement)
	}

	return statement, nil
}

// resumeIndex skips a concurrent build that finished and rebuilds one
// that left an invalid index.
func (r *Runner) resumeIndex(ctx context.Context, index, statement string) (string, error) {
	var valid *bool

	err := r.pool.QueryRow(ctx, `SELECT indisvalid FROM pg_catalog.pg_index
		WHERE indexrelid = to_regclass($1)`, index).Scan(&valid)

	switch {
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("resume: %w", err)
	case valid != nil && *valid:
		return "", nil
	case valid != nil:
		if err := r.leftover(ctx, statement); err != nil {
			return "", err
		}
	default:
	}

	return statement, nil
}

// resumeDetach finalizes a detach left pending and skips a finished one.
func (r *Runner) resumeDetach(ctx context.Context, partition pair, statement string) (string, error) {
	var pending *bool

	err := r.pool.QueryRow(ctx, `SELECT (SELECT inhdetachpending FROM pg_catalog.pg_inherits
		WHERE inhrelid = to_regclass($1) AND inhparent = to_regclass($2))`, partition.second, partition.first).
		Scan(&pending)

	switch {
	case err != nil:
		return "", fmt.Errorf("resume: %w", err)
	case pending == nil:
		return "", nil
	case *pending:
		return "ALTER TABLE " + partition.first + " DETACH PARTITION " + partition.second + " FINALIZE", nil
	default:
		return statement, nil
	}
}

// leftover drops the invalid index a failed concurrent build leaves.
func (r *Runner) leftover(ctx context.Context, statement string) error {
	index, ok := concurrentIndex(statement)
	if !ok {
		return nil
	}

	var invalid bool

	err := r.pool.QueryRow(context.WithoutCancel(ctx), `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_index
		WHERE indexrelid = to_regclass($1) AND NOT indisvalid)`, index).Scan(&invalid)
	if err != nil {
		return fmt.Errorf("leftover of %s: %w", index, err)
	}

	if !invalid {
		return nil
	}

	if _, err := r.pool.Exec(context.WithoutCancel(ctx), "DROP INDEX CONCURRENTLY IF EXISTS "+index); err != nil {
		return fmt.Errorf("drop invalid %s: %w", index, err)
	}

	return nil
}
