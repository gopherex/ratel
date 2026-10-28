package ratel

import (
	"slices"

	"github.com/jackc/pgx/v5"
)

// ErrNotLoaded means the caller did not request this relation or count.
const ErrNotLoaded refusal = "ratel: relation not loaded"

// Loaded distinguishes an unrequested relation from a loaded empty or present value.
// Generated model accessors delegate to Get; models carry no executor.
type Loaded[T any] struct {
	value  T
	loaded bool
}

// LoadedFrom constructs a successfully loaded relation value, including empty values.
func LoadedFrom[T any](value T) Loaded[T] { return Loaded[T]{value: value, loaded: true} }

// Get returns ErrNotLoaded until the relation was explicitly loaded.
func (l Loaded[T]) Get() (T, error) {
	if !l.loaded {
		var zero T

		return zero, ErrNotLoaded
	}

	return l.value, nil
}

// IsZero reports whether the relation was not requested.
func (l Loaded[T]) IsZero() bool { return !l.loaded }

// Cardinality describes the generated relation's maximum number of children.
type Cardinality uint8

const (
	// RelOne allows zero or one related model per parent.
	RelOne Cardinality = iota + 1
	// RelMany allows any number of related models per parent.
	RelMany
)

// RelKey projects one parent-key component into a native pgx array. Column is
// the matching foreign-key column on the target, or on the through table.
// Type is the schema-qualified PostgreSQL scalar type. Values preserves input order.
type RelKey[M any] struct {
	Column string
	Type   pgx.Identifier
	Values func([]M) any
}

// Relation is a generated relation descriptor or its count projection.
type Relation[M any] interface {
	ratelRelation(models []M) (loadJob, error)
}

// RelationTarget is a physical generated table with primary-key metadata.
// SELECT builders and static queries cannot silently become relation targets.
type RelationTarget[M any] interface{ ratelTable() Table[M] }

func (t Table[M]) ratelTable() Table[M] { return t }

// Rel describes an immutable, typed eager-loading edge.
type Rel[M, R any] struct {
	query         Select[R]
	cardinality   Cardinality
	keys          []RelKey[M]
	attach        func(*M, []R)
	count         func(*M, int64)
	through       pgx.Identifier
	targetColumns []string
	children      []Relation[R]
	strategy      LoadStrategy
}

// RelOption supplies generated relation metadata.
type RelOption[M, R any] func(Rel[M, R]) Rel[M, R]

// NewRel constructs a generated descriptor. The generated attach function writes
// LoadedFrom(children) into the parent's R field; a to-one attach maps empty to nil.
func NewRel[M, R any](target RelationTarget[R], cardinality Cardinality, keys []RelKey[M],
	attach func(*M, []R), options ...RelOption[M, R],
) Rel[M, R] {
	keys = slices.Clone(keys)
	for index := range keys {
		keys[index].Type = slices.Clone(keys[index].Type)
	}

	relation := Rel[M, R]{
		query:       Select[R]{source: target.ratelTable().ratelSource()},
		cardinality: cardinality, keys: keys, attach: attach,
	}
	for _, option := range options {
		relation = option(relation)
	}

	return relation
}

// RelCount supplies the generated count-field assignment, independently of loaded rows.
func RelCount[M, R any](attach func(*M, int64)) RelOption[M, R] {
	return func(relation Rel[M, R]) Rel[M, R] {
		relation.count = attach

		return relation
	}
}

// Through supplies the schema-qualified join table and its target-key columns,
// in the same order as the target's primary key. RelKey columns address this table.
func Through[M, R any](schema, table string, targetColumns ...string) RelOption[M, R] {
	columns := slices.Clone(targetColumns)

	return func(relation Rel[M, R]) Rel[M, R] {
		relation.through = pgx.Identifier{schema, table}
		relation.targetColumns = columns

		return relation
	}
}

// Where adds a child predicate without filtering parents.
func (r Rel[M, R]) Where(condition Cond[R]) Rel[M, R] {
	r.query = r.query.Where(condition)

	return r
}

// OrderBy appends ordering within each parent.
func (r Rel[M, R]) OrderBy(orders ...Order[R]) Rel[M, R] {
	r.query = r.query.OrderBy(orders...)

	return r
}

// Limit caps children per parent, not the whole batch.
func (r Rel[M, R]) Limit(count int) Rel[M, R] {
	r.query = r.query.Limit(count)

	return r
}

// Offset skips children independently for each parent.
func (r Rel[M, R]) Offset(count int) Rel[M, R] {
	r.query = r.query.Offset(count)

	return r
}

// With requests nested relations, loaded in a later depth batch before attachment.
func (r Rel[M, R]) With(relations ...Relation[R]) Rel[M, R] {
	r.children = append(slices.Clone(r.children), relations...)

	return r
}

// WithCount requests independent counts on the related models.
func (r Rel[M, R]) WithCount(relations ...CountRelation[R]) Rel[M, R] {
	return r.With(countRelations(relations)...)
}

// LoadStrategy selects the SQL shape of a relation load.
type LoadStrategy struct{ kind loadStrategy }

type loadStrategy uint8

const (
	strategyDefault loadStrategy = iota
	strategyWindow
	strategyCorrelated
	strategyJoined
)

// Window uses row_number partitioned by parent for per-parent pagination.
func Window() LoadStrategy { return LoadStrategy{kind: strategyWindow} }

// Correlated uses one correlated count subquery per parent in the count statement.
func Correlated() LoadStrategy { return LoadStrategy{kind: strategyCorrelated} }

// Joined joins the parent-key input to a to-one target in the depth batch.
func Joined() LoadStrategy { return LoadStrategy{kind: strategyJoined} }

// Strategy overrides the default batched or LATERAL strategy for this edge.
func (r Rel[M, R]) Strategy(strategy LoadStrategy) Rel[M, R] {
	r.strategy = strategy

	return r
}

// CountRelation can project a generated relation's count field.
type CountRelation[M any] interface{ Count() Relation[M] }

type countRelation[M, R any] struct{ relation Rel[M, R] }

// Count requests the count only. Child pagination and nested loads do not affect it.
func (r Rel[M, R]) Count() Relation[M] { return countRelation[M, R]{relation: r} }

func countRelations[M any](relations []CountRelation[M]) []Relation[M] {
	result := make([]Relation[M], len(relations))

	for index, relation := range relations {
		if relation != nil {
			result[index] = relation.Count()
		}
	}

	return result
}
