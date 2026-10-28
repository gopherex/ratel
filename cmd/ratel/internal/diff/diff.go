// Package diff computes the changes from one database state to another.
// Both sides are renderings a PostgreSQL server normalized: each was applied
// to its own dev database and introspected, so equal objects render equally.
// Objects are compared by the key and statement introspection renders them
// with; table, view and domain internals are compared part by part.
package diff

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// Intents are the schema annotations that change what a difference means.
type Intents struct {
	// Renamed maps a new object to its old name: "table s.new" and "column
	// s.t.new" to the old identifier, "value s.type 'new'" to the old label
	// literal.
	Renamed map[string]string
	// Retype maps "column s.t.c" to the USING expression of its type change.
	Retype map[string]string
}

// delta is the changes one object needs: those made in creation order and
// those dropped with the object's own drops.
type delta struct{ changes, drops []Change }

// Diff returns the changes that turn current into desired, ordered so each
// change's dependencies exist before it and its dependents are gone before a
// drop: renames first, creates and alterations in creation order, then drops
// in reverse.
func Diff(current, desired introspect.Rendering, intents Intents) []Change {
	changes, current := renameTables(current, desired, intents)
	before := statements(current)
	after := statements(desired)
	deferred := make(map[string][]Change)

	for _, object := range desired.Objects {
		previous, exists := before[object.Key]

		switch {
		case !exists:
			changes = append(changes, created(object))
		case previous != object.SQL || columnsDiffer(current, desired, object.Key):
			step := altered(current, desired, intents, object.Key, previous, object.SQL)
			changes = append(changes, step.changes...)
			deferred[object.Key] = step.drops
		default:
		}
	}

	var drops []Change

	for _, object := range slices.Backward(current.Objects) {
		drops = append(drops, deferred[object.Key]...)

		if _, exists := after[object.Key]; !exists {
			drops = append(drops, dropped(object))
		}
	}

	return renamedObjects(changes, drops)
}

// columnsDiffer reports a table whose columns differ although its statement
// does not: a partition's statement has no columns.
func columnsDiffer(current, desired introspect.Rendering, key string) bool {
	kind, name := kindOf(key), nameOf(key)

	return kind == tableObject && !slices.Equal(columnsOf(current, name), columnsOf(desired, name))
}

func statements(rendering introspect.Rendering) map[string]string {
	result := make(map[string]string, len(rendering.Objects))
	for _, object := range rendering.Objects {
		result[object.Key] = object.SQL
	}

	return result
}

// object is the kind of object a rendering key starts with.
type objectKind string

const (
	schemaObject     objectKind = "schema"
	extensionObject  objectKind = "extension"
	typeObject       objectKind = "type"
	sequenceObject   objectKind = "sequence"
	routineObject    objectKind = "routine"
	tableObject      objectKind = "table"
	constraintObject objectKind = "constraint"
	rlsObject        objectKind = "rls"
	viewObject       objectKind = "view"
	indexObject      objectKind = "index"
	triggerObject    objectKind = "trigger"
	policyObject     objectKind = "policy"
	commentObject    objectKind = "comment"
	grantObject      objectKind = "grant"
)

// key is the rendering key of an object of a kind.
func (k objectKind) key(name string) string { return string(k) + " " + name }

// kindOf is the object kind of a rendering key.
func kindOf(key string) objectKind {
	kind, _, _ := strings.Cut(key, " ")

	return objectKind(kind)
}

// nameOf is the object name of a rendering key.
func nameOf(key string) string {
	_, name, _ := strings.Cut(key, " ")

	return name
}

// parentOf is the "schema.table" of a "schema.table.child" name.
func parentOf(name string) string {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		return name[:dot]
	}

	return name
}

// replaced is the old object dropped and the new one created.
func replaced(key, before, after string) []Change {
	return []Change{
		dropped(introspect.Object{Key: key, SQL: before}),
		created(introspect.Object{Key: key, SQL: after}),
	}
}

func created(object introspect.Object) Change {
	kind, name := kindOf(object.Key), nameOf(object.Key)
	change := Change{Kind: createKind(kind, object.SQL), Key: object.Key, SQL: object.SQL, After: object.SQL}

	switch kind {
	case tableObject, rlsObject:
		change.Table = name
	case constraintObject, triggerObject, policyObject:
		change.Table = parentOf(name)
	case indexObject:
		change.Table = indexTable(object.SQL)
	default:
	}

	return change
}

func createKind(kind objectKind, sql string) Kind {
	switch {
	case kind == tableObject && strings.Contains(sql, " PARTITION OF "):
		return AttachPartition
	case kind == constraintObject:
		return constraintKind(sql)
	default:
		return creates(kind)
	}
}

// creates is the change kind creating an object of a kind.
func creates(kind objectKind) Kind {
	switch kind {
	case schemaObject:
		return CreateSchema
	case extensionObject:
		return CreateExtension
	case typeObject:
		return CreateType
	case sequenceObject:
		return CreateSequence
	case routineObject:
		return CreateRoutine
	case tableObject:
		return CreateTable
	case indexObject:
		return CreateIndex
	case viewObject:
		return CreateView
	case triggerObject:
		return CreateTrigger
	case policyObject:
		return CreatePolicy
	case rlsObject:
		return EnableRLS
	case grantObject:
		return Grant
	default:
		return Comment
	}
}

// constraintKind classifies an ADD CONSTRAINT statement by its definition.
func constraintKind(sql string) Kind {
	_, definition, _ := strings.Cut(sql, " ADD CONSTRAINT ")

	switch {
	case strings.Contains(definition, " PRIMARY KEY "):
		return AddPrimaryKey
	case strings.Contains(definition, " UNIQUE "):
		return AddUnique
	case strings.Contains(definition, " FOREIGN KEY "):
		return AddForeignKey
	case strings.Contains(definition, " EXCLUDE "):
		return AddExclusion
	default:
		return AddCheck
	}
}

// indexTable is the table of a CREATE INDEX statement.
func indexTable(sql string) string {
	_, rest, _ := strings.Cut(sql, " ON ")
	rest = strings.TrimPrefix(rest, "ONLY ")
	table, _, _ := strings.Cut(rest, " ")

	return table
}
