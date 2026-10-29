package ratel

import (
	"context"
	"iter"
	"slices"
)

// EagerSelect is a SELECT with requested relations. It deliberately has no
// All verb after db.Run and does not implement Source: eager work cannot be
// discarded by nesting.
type EagerSelect[M any] struct {
	query     Select[M]
	relations []Relation[M]
}

// With switches to an eager SELECT, whose runner excludes the row iterator.
func (q Select[M]) With(relations ...Relation[M]) EagerSelect[M] {
	return EagerSelect[M]{query: q, relations: slices.Clone(relations)}
}

// WithCount requests only the generated count fields of these relations.
func (q Select[M]) WithCount(relations ...CountRelation[M]) EagerSelect[M] {
	return q.With(countRelations(relations)...)
}

// With appends relations without changing existing builder branches.
func (q EagerSelect[M]) With(relations ...Relation[M]) EagerSelect[M] {
	q.relations = append(slices.Clone(q.relations), relations...)

	return q
}

// WithCount appends independent relation-count projections.
func (q EagerSelect[M]) WithCount(relations ...CountRelation[M]) EagerSelect[M] {
	return q.With(countRelations(relations)...)
}

// Where adds a parent predicate.
func (q EagerSelect[M]) Where(condition Cond[M]) EagerSelect[M] {
	q.query = q.query.Where(condition)

	return q
}

// OrderBy appends parent ordering terms.
func (q EagerSelect[M]) OrderBy(orders ...Order[M]) EagerSelect[M] {
	q.query = q.query.OrderBy(orders...)

	return q
}

// Limit caps the number of parents.
func (q EagerSelect[M]) Limit(count int) EagerSelect[M] {
	q.query = q.query.Limit(count)

	return q
}

// Offset skips parents, not children.
func (q EagerSelect[M]) Offset(count int) EagerSelect[M] {
	q.query = q.query.Offset(count)

	return q
}

// Build renders the root query only: relation queries bind the parent keys,
// which exist only after it runs. Observe them with rateltest.Recorder.
func (q EagerSelect[M]) Build() (string, []any, error) { return q.query.Build() }

// EagerRunner is an eager SELECT bound to a database by db.Run.
type EagerRunner[M any] struct {
	db    *DB
	eager EagerSelect[M]
}

func (q EagerSelect[M]) bind(db *DB) EagerRunner[M] { return EagerRunner[M]{db: db, eager: q} }

// List reads parents, closes their cursor, then loads every requested depth.
// A failure returns no partial slice.
func (r EagerRunner[M]) List(ctx context.Context, options ...CallOption) ([]M, error) {
	if err := r.validate(options); err != nil {
		return nil, err
	}

	models, err := SelectRunner[M]{db: r.db, query: r.eager.query}.List(ctx, options...)
	if err == nil {
		err = r.loadModels(ctx, models)
	}

	if err != nil {
		return nil, err
	}

	return models, nil
}

func (r EagerRunner[M]) validate(options []CallOption) error {
	if err := r.db.validateLoadMode(options); err != nil {
		return err
	}

	_, err := prepareRelations([]M{}, r.eager.relations)

	return err
}

func (r EagerRunner[M]) loadModels(ctx context.Context, models []M) error {
	jobs, err := prepareRelations(models, r.eager.relations)
	if err != nil {
		return err
	}

	return r.db.load(ctx, jobs)
}

// One requires exactly one parent before any relations are loaded.
func (r EagerRunner[M]) One(ctx context.Context, options ...CallOption) (M, error) {
	const detectMultiple = 2

	return r.single(ctx, detectMultiple, options)
}

// First loads the first parent and its relations, or returns ErrNotFound.
func (r EagerRunner[M]) First(ctx context.Context, options ...CallOption) (M, error) {
	return r.single(ctx, 1, options)
}

func (r EagerRunner[M]) single(ctx context.Context, limit int, options []CallOption) (M, error) {
	var zero M
	if err := r.validate(options); err != nil {
		return zero, err
	}

	query := r.eager.query
	if !query.verbatim() && (!query.limit.Set || query.limit.V > limit) {
		query = query.Limit(limit)
	}
	// Check cardinality before loading children, so invalid One results do no eager I/O.
	parents := SelectRunner[M]{db: r.db, query: query}

	models, err := parents.collect(ctx, limit, options)
	if err != nil {
		return zero, err
	}

	if len(models) == 0 {
		return zero, ErrNotFound
	}

	if len(models) > 1 {
		return zero, ErrMultipleRows
	}

	if err := r.loadModels(ctx, models); err != nil {
		return zero, err
	}

	return models[0], nil
}

// Chunks loads each keyset page only after its parent cursor has closed.
// Each page is fully loaded before it is yielded; no per-row lazy I/O occurs.
func (r EagerRunner[M]) Chunks(ctx context.Context, size int, options ...CallOption) iter.Seq2[[]M, error] {
	return func(yield func([]M, error) bool) {
		if err := r.validate(options); err != nil {
			yield(nil, err)

			return
		}

		for models, err := range (SelectRunner[M]{db: r.db, query: r.eager.query}).Chunks(ctx, size, options...) {
			if err == nil {
				err = r.loadModels(ctx, models)
			}

			if err != nil {
				yield(nil, err)

				return
			}

			if !yield(models, nil) {
				return
			}
		}
	}
}
