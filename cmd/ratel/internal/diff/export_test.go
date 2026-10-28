package diff

import "github.com/gopherex/ratel/cmd/ratel/internal/introspect"

// Step is the changes an object needs and those dropped with its drops.
type Step struct{ Changes, Drops []Change }

func step(result delta) Step { return Step{Changes: result.changes, Drops: result.drops} }

// Altered exposes altered to tests.
func Altered(current, desired introspect.Rendering, intents Intents, key, before, after string) Step {
	return step(altered(current, desired, intents, key, before, after))
}

// AlteredTable exposes alteredTable to tests.
func AlteredTable(current, desired introspect.Rendering, intents Intents, name, before, after string) Step {
	return step(alteredTable(current, desired, intents, name, before, after))
}

// AlteredColumns exposes alteredColumns to tests.
func AlteredColumns(current, desired introspect.Rendering, intents Intents, table string) Step {
	return step(alteredColumns(current, desired, intents, table))
}

// AlteredColumn exposes alteredColumn to tests for column app.t.c.
func AlteredColumn(major int, before, after introspect.Column, intents Intents) []Change {
	return alteredColumn(columnEdit{table: "app.t", name: "c", major: major}, &before, &after, intents)
}

// Exposed internals.
func PartitionColumns(current, desired introspect.Rendering, table string) []Change {
	return partitionColumns(current, desired, table)
}

func ColumnsDiffer(current, desired introspect.Rendering, key string) bool {
	return columnsDiffer(current, desired, key)
}

func KindOf(key string) string { return string(kindOf(key)) }
func NameOf(key string) string { return nameOf(key) }

func ParentOf(name string) string                     { return parentOf(name) }
func Created(object introspect.Object) Change         { return created(object) }
func Dropped(object introspect.Object) Change         { return dropped(object) }
func AlteredRLS(key, name, after string) Change       { return alteredRLS(key, name, after) }
func SequenceAlteration(create string) string         { return alterSequence(create) }
func Signature(definition string) string              { return signature(definition) }
func IdentityOptions(clause string) string            { return identityOptions(clause) }
func PlainType(name string) string                    { return plainType(name) }
func EnumLabels(sql string) []string                  { return enumLabels(sql) }
func Requalify(text, from, target string) string      { return requalify(text, from, target) }
func RenamedObjects(changes, drops []Change) []Change { return renamedObjects(changes, drops) }
func AppendsColumns(current, desired introspect.Rendering, view string) bool {
	return appendsColumns(current, desired, view)
}

func AlteredEnum(intents Intents, key, name, before, after string) []Change {
	return alteredEnum(intents, key, name, before, after)
}

func AlteredType(current, desired introspect.Rendering, intents Intents, key, before, after string) []Change {
	return alteredType(current, desired, intents, key, before, after)
}

func AlteredDomain(key, name string, before, after introspect.Domain) []Change {
	return alteredDomain(key, name, before, after)
}

func DomainOf(rendering introspect.Rendering, key string) bool {
	_, ok := domainOf(rendering, key)

	return ok
}

func RenameTables(current, desired introspect.Rendering, intents Intents) ([]Change, introspect.Rendering) {
	return renameTables(current, desired, intents)
}
