package plan

import (
	"slices"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// usage is an object using a replaced one: dropped before it and created
// again after it from its current definition.
type usage struct{ drop, create diff.Change }

// users are the objects using key, transitively, in rendering order; false
// when one of them is not an object the planner can recreate.
func (p *planner) users(key string) ([]usage, bool) {
	seen := map[string]bool{key: true}
	pending := []string{key}

	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]

		for _, dependent := range p.env.Dependents[next] {
			if seen[dependent] {
				continue
			}

			if !recreatable(dependent) {
				return nil, false
			}

			seen[dependent] = true

			pending = append(pending, dependent)
		}
	}

	var result []usage

	for _, object := range p.env.Current.Objects {
		if object.Key == key || !seen[object.Key] {
			continue
		}

		// A user the diff changes itself is replaced by its own change.
		if _, desired := p.env.Desired.Statement(object.Key); !desired || p.changed(object) {
			continue
		}

		result = append(result, usage{
			drop:   diff.Dropped(object),
			create: diff.Created(introspect.Object{Key: object.Key, SQL: object.SQL}),
		})
	}

	return result, true
}

// changed reports an object whose desired statement differs from the
// current one.
func (p *planner) changed(object introspect.Object) bool {
	desired, _ := p.env.Desired.Statement(object.Key)

	return desired != object.SQL
}

// recreatable is a user kind that can be dropped and created again without
// losing data.
func recreatable(key string) bool {
	return slices.Contains([]string{viewObject, routineObject, policyObject, triggerObject}, kindOf(key))
}
