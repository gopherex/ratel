package ratel

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type loadJob struct {
	statement Statement
	read      func(pgx.Rows) error
	children  func() ([]loadJob, error)
	attach    func()
}

type relationResult[R any] struct {
	models      []R
	parents     []int
	counts      []int
	parentCount int
}

type relationRow struct {
	row    pgx.Row
	parent *int
}

func (r relationRow) Scan(dest ...any) error {
	if err := r.row.Scan(append([]any{r.parent}, dest...)...); err != nil {
		return fmt.Errorf("ratel: scan relation: %w", err)
	}

	return nil
}

func (r Rel[M, R]) ratelRelation(models []M) (loadJob, error) {
	statement, err := r.statement(models, false)
	if err != nil {
		return loadJob{}, err
	}

	result := &relationResult[R]{parentCount: len(models), counts: make([]int, len(models))}

	return loadJob{
		statement: statement,
		read:      func(rows pgx.Rows) error { return r.readRelated(rows, result) },
		children:  func() ([]loadJob, error) { return prepareRelations(result.models, r.children) },
		attach:    func() { r.attachRelated(models, result) },
	}, nil
}

func (r Rel[M, R]) readRelated(rows pgx.Rows, result *relationResult[R]) error {
	for rows.Next() {
		var parent int

		model, err := r.query.source.table.scan(relationRow{row: rows, parent: &parent})
		if err != nil {
			return fmt.Errorf("ratel: scan related model: %w", err)
		}

		if parent <= 0 || parent > result.parentCount {
			return ErrInvalidQuery
		}

		parent--

		result.counts[parent]++
		if r.cardinality == RelOne && result.counts[parent] > 1 {
			return ErrMultipleRows
		}

		result.models = append(result.models, model)
		result.parents = append(result.parents, parent)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("ratel: iterate relation: %w", err)
	}

	return nil
}

func (r Rel[M, R]) attachRelated(models []M, result *relationResult[R]) {
	groups := make([][]R, len(models))
	for index := range groups {
		groups[index] = make([]R, 0, result.counts[index])
	}

	for index, model := range result.models {
		parent := result.parents[index]
		groups[parent] = append(groups[parent], model)
	}

	for index := range models {
		r.attach(&models[index], groups[index])
	}
}

func (r countRelation[M, R]) ratelRelation(models []M) (loadJob, error) {
	statement, err := r.relation.statement(models, true)
	if err != nil {
		return loadJob{}, err
	}

	counts := make([]int64, len(models))

	return loadJob{
		statement: statement,
		read:      func(rows pgx.Rows) error { return readCounts(rows, counts) },
		attach: func() {
			for index := range models {
				r.relation.count(&models[index], counts[index])
			}
		},
	}, nil
}

func readCounts(rows pgx.Rows, counts []int64) error {
	for rows.Next() {
		var parent int

		var count int64
		if err := rows.Scan(&parent, &count); err != nil {
			return fmt.Errorf("ratel: scan relation count: %w", err)
		}

		if parent <= 0 || parent > len(counts) || count < 0 {
			return ErrInvalidQuery
		}

		counts[parent-1] = count
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("ratel: iterate relation counts: %w", err)
	}

	return nil
}

func prepareRelations[M any](models []M, relations []Relation[M]) ([]loadJob, error) {
	jobs := make([]loadJob, 0, len(relations))

	for _, relation := range relations {
		if relation == nil {
			return nil, ErrInvalidQuery
		}

		job, err := relation.ratelRelation(models)
		if err != nil {
			return nil, err
		}

		jobs = append(jobs, job)
	}

	if len(models) == 0 {
		return nil, nil
	}

	return jobs, nil
}

// Load eagerly loads relations on any slice, including static-query results.
// All depth batches close before the next depth starts. Attachments occur only
// after every depth succeeds, deepest first; failure leaves the slice unchanged.
func (db *DB) Load[M any](ctx context.Context, models []M, relations ...Relation[M]) error {
	if err := db.validateLoadMode(nil); err != nil {
		return err
	}

	jobs, err := prepareRelations(models, relations)
	if err != nil {
		return err
	}

	ctx, span := db.tracer.Start(ctx, "ratel.load", readSpanOptions("", "")...)
	defer span.End()

	if err := db.load(ctx, jobs); err != nil {
		recordFailure(span, err)

		return err
	}

	return nil
}

func (db *DB) load(ctx context.Context, jobs []loadJob) error {
	var completed []loadJob

	for len(jobs) > 0 {
		if err := db.loadDepth(ctx, jobs); err != nil {
			return err
		}

		next := make([]loadJob, 0)

		for _, job := range jobs {
			if job.children == nil {
				continue
			}

			children, err := job.children()
			if err != nil {
				return err
			}

			next = append(next, children...)
		}

		completed = append(completed, jobs...)
		jobs = next
	}

	for index := len(completed) - 1; index >= 0; index-- {
		completed[index].attach()
	}

	return nil
}

func (db *DB) loadDepth(ctx context.Context, jobs []loadJob) error {
	ctx, span := db.tracer.Start(ctx, "ratel.load.depth", readSpanOptions("", "")...)
	defer span.End()

	batch := &pgx.Batch{}

	for _, job := range jobs {
		batch.Queue(job.statement.SQL, job.statement.Args...)
	}

	results := db.executor.SendBatch(ctx, batch)

	err := consumeRelations(results, jobs)
	if err != nil {
		recordFailure(span, err)
	}

	return err
}

func (db *DB) validateLoadMode(options []CallOption) error {
	if db == nil {
		return ErrInvalidQuery
	}

	settings := callSettings{mode: db.execMode}
	for _, option := range options {
		settings = option(settings)
	}
	// pgx Batch.Queue supports QueryRewriter, not per-query QueryExecMode.
	// Reject explicit overrides instead of binding them as SQL arguments or ignoring them.
	if settings.mode.Set {
		return ErrInvalidQuery
	}

	return nil
}

func consumeRelations(results pgx.BatchResults, jobs []loadJob) error {
	for _, job := range jobs {
		rows, err := results.Query()
		if err != nil {
			return fmt.Errorf("ratel: query relation batch: %w", errors.Join(err, results.Close()))
		}

		err = job.read(rows)
		rows.Close()

		if err != nil {
			return fmt.Errorf("ratel: read relation batch: %w", errors.Join(err, results.Close()))
		}
	}

	if err := results.Close(); err != nil {
		return fmt.Errorf("ratel: close relation batch: %w", err)
	}

	return nil
}
