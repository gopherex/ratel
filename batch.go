package ratel

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// BatchItem is one operation of db.Batch with the destination of its result.
// An item that cannot render keeps its error, and db.Batch returns it before
// anything is sent.
type BatchItem struct {
	plan   plan
	handle func(pgx.BatchResults) error // reads one result per statement, in order
	err    error
}

// Batch sends the items in one round trip and fills each item's destination
// in order. pgx batches use the connection's query exec mode; per-call
// ExecMode does not apply. The first failure is returned; integrity
// violations are *ConstraintError as in single writes.
func (db *DB) Batch(ctx context.Context, items ...BatchItem) error {
	if db == nil {
		return ErrInvalidQuery
	}

	batch := &pgx.Batch{}

	for _, item := range items {
		if item.err != nil {
			return item.err
		}

		for index := range item.plan.count() {
			rendered := item.plan.at(index)

			batch.Queue(rendered.SQL, rendered.Args...)
		}
	}

	if batch.Len() == 0 {
		return nil
	}

	results := db.executor.SendBatch(ctx, batch)

	var err error

	for _, item := range items {
		if err = item.handle(results); err != nil {
			break
		}
	}

	if closeErr := results.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("ratel: batch: %w", closeErr)
	}

	return err
}

// List is a batch item that collects every row of a row query into dest; an
// empty result is a non-nil slice. Only values that return rows are accepted.
func List[M any, Q RowQuery[M]](query Q, dest *[]M) BatchItem {
	return rowsItem(query.rowsPlan(), dest, nil)
}

// One is a batch item that scans the single row of a row query into dest:
// ErrNotFound (or the write's refusal) for none, ErrMultipleRows for more.
// Only values that return rows are accepted.
func One[M any, Q RowQuery[M]](query Q, dest *M) BatchItem {
	return rowsItem(query.rowsPlan(), nil, dest)
}

// Exec is a batch item that stores the affected rows of a write, an insert
// or a Command in count, with the missed-row refusals of Save and DeleteRow.
// Only values that do not return rows are accepted.
func Exec[Q ExecQuery](query Q, count *int64) BatchItem {
	planned := query.execPlan()
	if planned.err != nil {
		return BatchItem{err: planned.err}
	}

	return BatchItem{plan: planned.plan, handle: func(results pgx.BatchResults) error {
		var total int64

		for range planned.count() {
			tag, err := results.Exec()
			if err != nil {
				return mapWriteError(err, planned.target)
			}

			total += tag.RowsAffected()
		}

		if total == 0 && planned.missing != "" {
			return planned.missing
		}

		*count = total

		return nil
	}}
}

// rowsItem reads one result per statement into one slice and stores it in
// list, or, for a single-row item, the only row in one.
func rowsItem[M any](planned rowsPlan[M], list *[]M, one *M) BatchItem {
	if planned.err != nil {
		return BatchItem{err: planned.err}
	}

	return BatchItem{plan: planned.plan, handle: func(results pgx.BatchResults) error {
		runner := RowRunner[M]{plan: planned}
		models := make([]M, 0)

		for range planned.count() {
			rows, err := results.Query()
			if err != nil {
				return runner.fail(err, "batch query")
			}

			err = readBatchRows(rows, runner, &models)
			rows.Close()

			if err != nil {
				return err
			}

			if err = rows.Err(); err != nil {
				return runner.fail(err, "batch iterate")
			}
		}

		return storeBatchRows(planned.missing, models, list, one)
	}}
}

// storeBatchRows hands the rows of a batch item to its destination.
func storeBatchRows[M any](missing refusal, models []M, list *[]M, one *M) error {
	if list != nil {
		if len(models) == 0 && missing != "" {
			return missing
		}

		*list = models

		return nil
	}

	switch len(models) {
	case 0:
		if missing != "" {
			return missing
		}

		return ErrNotFound
	case 1:
		*one = models[0]

		return nil
	default:
		return ErrMultipleRows
	}
}

func readBatchRows[M any](rows pgx.Rows, runner RowRunner[M], models *[]M) error {
	for rows.Next() {
		model, err := runner.scan(rows)
		if err != nil {
			return err
		}

		*models = append(*models, model)
	}

	return nil
}
