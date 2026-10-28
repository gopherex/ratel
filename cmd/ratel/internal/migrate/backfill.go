package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBackfill is a backfill statement the runner cannot batch.
var ErrBackfill = errors.New("backfill cannot be batched")

// progress is the resume_key of a backfill: the statement it is on and the
// key of the last row of its last committed batch.
type progress struct {
	Statement int      `json:"statement"`
	Key       []string `json:"key"`
}

// keyColumn is a batching key column and its type.
type keyColumn struct{ name, kind string }

// backfill runs each UPDATE of a step in batches of the table's key order,
// one transaction per batch, recording after each the last key it reached.
func (r *Runner) backfill(ctx context.Context, step *Step, resume string) error {
	var done progress

	if resume != "" {
		if err := json.Unmarshal([]byte(resume), &done); err != nil {
			return fmt.Errorf("resume key: %w", err)
		}
	}

	for index := done.Statement; index < len(step.Statements); index++ {
		update, err := splitUpdate(step.Statements[index])
		if err != nil {
			return err
		}

		key, err := r.key(ctx, update.table, step.Key)
		if err != nil {
			return err
		}

		lower := done.Key
		if index != done.Statement {
			lower = nil
		}

		if err := r.batches(ctx, step.ID, index, update, key, lower); err != nil {
			return err
		}
	}

	return nil
}

// update is an UPDATE split around its WHERE clause.
type update struct{ table, head, condition string }

// splitUpdate reads the table of an UPDATE and cuts it at its top-level
// WHERE.
func splitUpdate(statement string) (update, error) {
	fields := strings.Fields(statement)
	if len(fields) < 3 || !strings.EqualFold(fields[0], "UPDATE") {
		return update{}, fmt.Errorf("%w: not an UPDATE: %s", ErrBackfill, firstLine(statement))
	}

	table := fields[1]
	if strings.EqualFold(table, "ONLY") {
		table = fields[2]
	}

	where := topLevelWhere(statement)
	if where < 0 {
		return update{table: table, head: statement, condition: "true"}, nil
	}

	return update{
		table:     table,
		head:      strings.TrimSpace(statement[:where]),
		condition: strings.TrimSpace(statement[where+len("WHERE"):]),
	}, nil
}

// topLevelWhere is the index of the last WHERE keyword outside parentheses,
// quotes and comments, or -1.
func topLevelWhere(statement string) int {
	found, depth := -1, 0

	for index := 0; index < len(statement); {
		if next := skip(statement, index); next > index {
			if depth == 0 && strings.EqualFold(statement[index:next], "WHERE") {
				found = index
			}

			index = next

			continue
		}

		switch statement[index] {
		case '(':
			depth++
		case ')':
			depth--
		default:
		}

		index++
	}

	return found
}

// key is the batching key: the step's, or the table's primary key, with
// the type of each column.
func (r *Runner) key(ctx context.Context, table string, columns []string) ([]keyColumn, error) {
	rows, err := r.pool.Query(ctx, `
SELECT quote_ident(a.attname), pg_catalog.format_type(a.atttypid, a.atttypmod)
FROM pg_catalog.pg_attribute a
LEFT JOIN pg_catalog.pg_index i ON i.indrelid = a.attrelid AND i.indisprimary
WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
  AND (CASE WHEN cardinality($2::text[]) > 0 THEN quote_ident(a.attname) = ANY($2)
       ELSE a.attnum = ANY(i.indkey) END)
ORDER BY CASE WHEN cardinality($2::text[]) > 0 THEN array_position($2, quote_ident(a.attname))
              ELSE array_position(i.indkey::int2[], a.attnum) END`, table, columns)
	if err != nil {
		return nil, fmt.Errorf("backfill key: %w", err)
	}

	key, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (keyColumn, error) {
		var column keyColumn

		return column, row.Scan(&column.name, &column.kind)
	})
	if err != nil {
		return nil, fmt.Errorf("backfill key: %w", err)
	}

	if len(key) == 0 {
		return nil, fmt.Errorf("%w: %s has no primary key", ErrBackfill, table)
	}

	return key, nil
}

// batches updates the rows after lower in batches until none are left.
func (r *Runner) batches(
	ctx context.Context, stepID string, statement int, update update, key []keyColumn, lower []string,
) error {
	for {
		upper, err := r.upper(ctx, update.table, key, lower)
		if err != nil {
			return err
		}

		if err := r.batch(ctx, stepID, statement, update, key, lower, upper); err != nil {
			return err
		}

		if upper == nil {
			return nil
		}

		lower = upper

		if err := r.waitReplicas(ctx); err != nil {
			return err
		}

		if err := r.pause(ctx, r.settings.Backfill.Pause); err != nil {
			return err
		}
	}
}

// columns is the key as a row expression.
func columns(key []keyColumn) string {
	names := make([]string, 0, len(key))
	for _, column := range key {
		names = append(names, column.name)
	}

	return "(" + strings.Join(names, ", ") + ")"
}

// parameters is the row of placeholders from first, cast to the key types.
func parameters(key []keyColumn, first int) string {
	values := make([]string, 0, len(key))
	for index, column := range key {
		values = append(values, "$"+strconv.Itoa(first+index)+"::text::"+column.kind)
	}

	return "(" + strings.Join(values, ", ") + ")"
}

func arguments(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}

	return result
}

// upper is the key of the last row of the next batch, or nil when the rows
// after lower fit in one.
func (r *Runner) upper(ctx context.Context, table string, key []keyColumn, lower []string) ([]string, error) {
	texts := make([]string, 0, len(key))
	for _, column := range key {
		texts = append(texts, column.name+"::text")
	}

	query := "SELECT " + strings.Join(texts, ", ") + " FROM " + table
	if lower != nil {
		query += " WHERE " + columns(key) + " > " + parameters(key, 1)
	}

	query += " ORDER BY " + strings.Trim(columns(key), "()") + " OFFSET " +
		strconv.Itoa(r.settings.Backfill.Batch-1) + " LIMIT 1"

	values := make([]string, len(key))
	targets := make([]any, len(key))

	for index := range values {
		targets[index] = &values[index]
	}

	err := r.pool.QueryRow(ctx, query, arguments(lower)...).Scan(targets...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("backfill batch: %w", err)
	}

	return values, nil
}

// batch updates the rows after lower up to upper in one transaction and
// records upper, retrying on lock timeouts like a short step.
func (r *Runner) batch(
	ctx context.Context, stepID string, statement int, update update, key []keyColumn, lower, upper []string,
) error {
	values := append(append([]string(nil), lower...), upper...)
	query := update.head + " WHERE (" + update.condition + ")"

	if lower != nil {
		query += " AND " + columns(key) + " > " + parameters(key, 1)
	}

	if upper != nil {
		query += " AND " + columns(key) + " <= " + parameters(key, len(lower)+1)
	}

	resume, err := json.Marshal(progress{Statement: statement, Key: upper})
	if err != nil {
		return fmt.Errorf("resume key: %w", err)
	}

	attempts := schedule(r.settings.LockTimeout)
	deadline := time.Now().Add(r.settings.MaxTotalWait)

	for index := 0; ; index++ {
		err := r.batchOnce(ctx, stepID, query, values, string(resume), attempts[min(index, len(attempts)-1)].timeout)

		var failure *pgconn.PgError
		if !errors.As(err, &failure) || failure.Code != lockNotAvailable || time.Now().After(deadline) {
			return err
		}

		if err := r.pause(ctx, r.jitter(attempts[min(index, len(attempts)-1)].pause)); err != nil {
			return err
		}
	}
}

func (r *Runner) batchOnce(ctx context.Context, stepID, query string, values []string, resume string,
	lockTimeout time.Duration,
) error {
	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	defer connection.Release()

	return inTransaction(ctx, connection.Conn(), func(transaction pgx.Tx) error {
		for _, setting := range []string{
			"SET LOCAL lock_timeout = " + milliseconds(lockTimeout),
			"SET LOCAL statement_timeout = " + milliseconds(r.settings.StatementTimeout),
			"SET LOCAL TimeZone = 'UTC'",
		} {
			if _, err := transaction.Exec(ctx, setting); err != nil {
				return fmt.Errorf("%s: %w", setting, err)
			}
		}

		if _, err := transaction.Exec(ctx, query, arguments(values)...); err != nil {
			return fmt.Errorf("backfill batch: %w", err)
		}

		_, err := transaction.Exec(ctx, `UPDATE ratel.migrations_journal SET resume_key = $2 WHERE step_id = $1`,
			stepID, resume)
		if err != nil {
			return fmt.Errorf("journal: %w", err)
		}

		return nil
	})
}

// waitReplicas pauses while any streaming replica lags more than
// max_replica_lag behind.
func (r *Runner) waitReplicas(ctx context.Context) error {
	for {
		var lag float64

		err := r.pool.QueryRow(ctx, `SELECT COALESCE(max(EXTRACT(EPOCH FROM replay_lag)), 0)::float8
			FROM pg_catalog.pg_stat_replication`).Scan(&lag)
		if err != nil {
			return fmt.Errorf("replica lag: %w", err)
		}

		if time.Duration(lag*float64(time.Second)) <= r.settings.Backfill.MaxReplicaLag {
			return nil
		}

		if err := r.pause(ctx, max(r.settings.Backfill.Pause, r.settings.LockTimeout)); err != nil {
			return err
		}
	}
}
