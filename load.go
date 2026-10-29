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
	models  []R
	counts  []int
	grouped bool // rows arrived in parent order so far
	// last is the parent of the last row while the rows are grouped.
	last int
	// parents is the parent of each model once the rows stop being grouped.
	parents     []int
	parentCount int
}

type relationRow struct {
	row    pgx.Row
	parent int
	dest   []any
}

func (r *relationRow) Scan(dest ...any) error {
	if len(dest)+1 != len(r.dest) {
		return ErrInvalidQuery
	}

	copy(r.dest[1:], dest)

	if err := r.row.Scan(r.dest...); err != nil {
		return fmt.Errorf("ratel: scan relation: %w", err)
	}

	return nil
}

func (r Rel[M, R]) ratelRelation(models []M) (loadJob, error) {
	statement, err := r.statement(models, false)
	if err != nil {
		return loadJob{}, err
	}

	result := &relationResult[R]{
		parentCount: len(models), counts: make([]int, len(models)),
		models: make([]R, 0, len(models)), grouped: true,
	}

	scan, columns, cardinality := r.query.source.table.scan, len(r.query.source.table.columns), r.cardinality
	children, attach := r.children, r.attach

	return loadJob{
		statement: statement,
		read:      func(rows pgx.Rows) error { return readRelated(rows, result, scan, columns, cardinality) },
		children:  func() ([]loadJob, error) { return prepareRelations(result.models, children) },
		attach:    func() { attachRelated(models, result, attach) },
	}, nil
}

func readRelated[R any](rows pgx.Rows, result *relationResult[R], scan Scanner[R], columns int,
	cardinality Cardinality,
) error {
	row := &relationRow{row: rows, dest: make([]any, columns+1)}
	row.dest[0] = &row.parent

	for rows.Next() {
		model, err := scan(row)
		if err != nil {
			return fmt.Errorf("ratel: scan related model: %w", err)
		}

		if row.parent <= 0 || row.parent > result.parentCount {
			return ErrInvalidQuery
		}

		parent := row.parent - 1

		result.counts[parent]++
		if cardinality == RelOne && result.counts[parent] > 1 {
			return ErrMultipleRows
		}

		result.add(model, parent)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("ratel: iterate relation: %w", err)
	}

	result.group()

	return nil
}

// add records a model of a parent whose count is already incremented.
// While rows arrive in parent order no parent list is kept: the parents of
// the rows so far follow from the counts.
func (result *relationResult[R]) add(model R, parent int) {
	switch {
	case result.grouped && parent >= result.last:
		result.last = parent
	case result.grouped:
		result.grouped = false
		result.parents = make([]int, 0, cap(result.models))

		for earlier, count := range result.counts {
			if earlier == parent {
				count-- // the current row is not appended yet
			}

			for range count {
				result.parents = append(result.parents, earlier)
			}
		}

		result.parents = append(result.parents, parent)
	default:
		result.parents = append(result.parents, parent)
	}

	result.models = append(result.models, model)
}

// group orders the models by parent, keeping each parent's rows in row
// order, which is the child order the SQL requested. The SQL leaves the
// grouping to it: sorting rows by parent on the server costs more than one
// counting pass here. Rows that arrived grouped are not copied.
func (result *relationResult[R]) group() {
	if result.grouped {
		return
	}

	next := make([]int, len(result.counts))
	offset := 0

	for parent, count := range result.counts {
		next[parent] = offset
		offset += count
	}

	grouped := make([]R, len(result.models))
	for row, parent := range result.parents {
		grouped[next[parent]] = result.models[row]
		next[parent]++
	}

	result.models, result.grouped = grouped, true
}

func attachRelated[M, R any](models []M, result *relationResult[R], attach func(*M, []R)) {
	// The models are grouped by parent. Keep each group's capacity bounded so
	// appending to one loaded relation cannot overwrite another.
	children := result.models

	for index, count := range result.counts {
		attach(&models[index], children[:count:count])
		children = children[count:]
	}
}

func (r countRelation[M, R]) ratelRelation(models []M) (loadJob, error) {
	statement, err := r.relation.statement(models, true)
	if err != nil {
		return loadJob{}, err
	}

	counts := make([]int64, len(models))
	attach := r.relation.count

	return loadJob{
		statement: statement,
		read:      func(rows pgx.Rows) error { return readCounts(rows, counts) },
		attach: func() {
			for index := range models {
				attach(&models[index], counts[index])
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

	return db.load(ctx, jobs)
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
	batch := &pgx.Batch{}

	for _, job := range jobs {
		batch.Queue(job.statement.SQL, job.statement.Args...)
	}

	return consumeRelations(db.executor.SendBatch(ctx, batch), jobs)
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
