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
	statements []Statement
	handle     func(pgx.BatchResults) error // reads one result per statement, in order
	err        error
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

		for _, statement := range item.statements {
			batch.Queue(statement.SQL, statement.Args...)
		}
	}

	if batch.Len() == 0 {
		return nil
	}

	ctx, span := db.tracer.Start(ctx, "ratel.batch", spanOptions("BATCH", "")...)
	defer span.End()

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

	if err != nil {
		return failure(span, err)
	}

	return nil
}

// Batch is the SELECT as a batch item that collects every row into dest.
func (q Select[M]) Batch(dest *[]M) BatchItem {
	statement, err := q.SQL()
	if err != nil {
		return BatchItem{err: err}
	}

	return BatchItem{statements: []Statement{statement}, handle: func(results pgx.BatchResults) error {
		rows, err := results.Query()
		if err != nil {
			return fmt.Errorf("ratel: batch SELECT: %w", err)
		}

		defer rows.Close()

		models := make([]M, 0)

		for rows.Next() {
			model, err := q.source.table.scan(rows)
			if err != nil {
				return fmt.Errorf("ratel: scan batch SELECT: %w", err)
			}

			models = append(models, model)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("ratel: iterate batch SELECT: %w", err)
		}

		*dest = models

		return nil
	}}
}

// BatchOne is the SELECT as a batch item that scans its single row into dest:
// ErrNotFound for none, ErrMultipleRows for more.
func (q Select[M]) BatchOne(dest *M) BatchItem {
	var models []M

	item := q.Batch(&models)
	if item.err != nil {
		return item
	}

	list := item.handle
	item.handle = func(results pgx.BatchResults) error {
		if err := list(results); err != nil {
			return err
		}

		switch len(models) {
		case 0:
			return ErrNotFound
		case 1:
			*dest = models[0]

			return nil
		default:
			return ErrMultipleRows
		}
	}

	return item
}

// Batch is the write as a batch item that stores the affected rows in count.
func (q Write[M]) Batch(count *int64) BatchItem {
	statement, err := q.SQL()
	if err != nil {
		return BatchItem{err: err}
	}

	return execItem([]Statement{statement}, q.table, q.missing, count)
}

// Batch is the insert as a batch item that stores the inserted rows in count.
func (q Insert[M, I]) Batch(count *int64) BatchItem {
	statements, err := q.Statements()
	if err != nil {
		return BatchItem{err: err}
	}

	return execItem(statements, q.table.Table, "", count)
}

// ExecItem is a static statement, as generated for :exec and :execrows
// queries, as a batch item that stores the affected rows in count.
func ExecItem(statement Statement, count *int64) BatchItem {
	if statement.SQL == "" {
		return BatchItem{err: ErrInvalidQuery}
	}

	return execItem([]Statement{statement}, Table[struct{}]{}, "", count)
}

func execItem[M any](statements []Statement, table Table[M], missing refusal, count *int64) BatchItem {
	return BatchItem{statements: statements, handle: func(results pgx.BatchResults) error {
		var total int64

		for range statements {
			tag, err := results.Exec()
			if err != nil {
				return mapWriteError(err, table)
			}

			total += tag.RowsAffected()
		}

		if total == 0 && missing != "" {
			return missing
		}

		*count = total

		return nil
	}}
}
