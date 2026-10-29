package ratel

import (
	"context"
	"iter"
	"slices"
)

// EagerSelect buffers parents before loading relations. It deliberately has no
// All method and does not implement Source: eager work cannot be discarded by nesting.
type EagerSelect[M any] struct {
	query     Select[M]
	relations []Relation[M]
}

// With switches to an eager SELECT, whose method set excludes the row iterator.
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

// List reads parents, closes their cursor, then loads every requested depth.
// A failure returns no partial slice.
func (q EagerSelect[M]) List(ctx context.Context, options ...CallOption) ([]M, error) {
	if err := q.validate(options); err != nil {
		return nil, err
	}

	models, err := q.query.List(ctx, options...)
	if err == nil {
		err = q.loadModels(ctx, models)
	}

	if err != nil {
		return nil, err
	}

	return models, nil
}

func (q EagerSelect[M]) validate(options []CallOption) error {
	if err := q.query.db.validateLoadMode(options); err != nil {
		return err
	}

	_, err := prepareRelations([]M{}, q.relations)

	return err
}

func (q EagerSelect[M]) loadModels(ctx context.Context, models []M) error {
	jobs, err := prepareRelations(models, q.relations)
	if err != nil {
		return err
	}

	return q.query.db.load(ctx, jobs)
}

// One requires exactly one parent before any relations are loaded.
func (q EagerSelect[M]) One(ctx context.Context, options ...CallOption) (M, error) {
	const detectMultiple = 2

	return q.single(ctx, detectMultiple, options)
}

// First loads the first parent and its relations, or returns ErrNotFound.
func (q EagerSelect[M]) First(ctx context.Context, options ...CallOption) (M, error) {
	return q.single(ctx, 1, options)
}

func (q EagerSelect[M]) single(ctx context.Context, limit int, options []CallOption) (M, error) {
	var zero M
	if err := q.validate(options); err != nil {
		return zero, err
	}

	if !q.query.verbatim() && (!q.query.limit.Set || q.query.limit.V > limit) {
		q.query = q.query.Limit(limit)
	}
	// Check cardinality before loading children, so invalid One results do no eager I/O.
	models, err := q.query.collect(ctx, limit, options)
	if err != nil {
		return zero, err
	}

	if len(models) == 0 {
		return zero, ErrNotFound
	}

	if len(models) > 1 {
		return zero, ErrMultipleRows
	}

	if err := q.loadModels(ctx, models); err != nil {
		return zero, err
	}

	return models[0], nil
}

// Chunks loads each keyset page only after its parent cursor has closed.
// Each page is fully loaded before it is yielded; no per-row lazy I/O occurs.
func (q EagerSelect[M]) Chunks(ctx context.Context, size int, options ...CallOption) iter.Seq2[[]M, error] {
	return func(yield func([]M, error) bool) {
		if err := q.validate(options); err != nil {
			yield(nil, err)

			return
		}

		for models, err := range q.query.Chunks(ctx, size, options...) {
			if err == nil {
				err = q.loadModels(ctx, models)
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
