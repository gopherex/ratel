package diff

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// altered is the changes between two renderings of one object. What has no
// statement altering it in place is replaced.
func altered(current, desired introspect.Rendering, intents Intents, key, before, after string) delta {
	kind, name := kindOf(key), nameOf(key)

	switch kind {
	case tableObject:
		return alteredTable(current, desired, intents, name, before, after)
	case typeObject:
		return delta{changes: alteredType(current, desired, intents, key, before, after)}
	case sequenceObject:
		return delta{changes: []Change{{
			Kind: AlterSequence, Key: key, Before: before, After: after, SQL: alterSequence(after),
		}}}
	case routineObject:
		if signature(before) == signature(after) {
			return delta{changes: []Change{{Kind: ReplaceRoutine, Key: key, Before: before, After: after, SQL: after}}}
		}
	case viewObject:
		if !materialized(before) && !materialized(after) && appendsColumns(current, desired, name) {
			replace := strings.Replace(after, "CREATE VIEW ", "CREATE OR REPLACE VIEW ", 1)

			return delta{changes: []Change{{Kind: ReplaceView, Key: key, Before: before, After: after, SQL: replace}}}
		}
	case rlsObject:
		return delta{changes: []Change{alteredRLS(key, name, after)}}
	case commentObject:
		return delta{changes: []Change{{Kind: Comment, Key: key, Before: before, After: after, SQL: after}}}
	case extensionObject:
		return delta{changes: []Change{alteredExtension(key, name, after)}}
	default:
	}

	return delta{changes: replaced(key, before, after)}
}

// signature is what CREATE OR REPLACE cannot change in a rendered routine:
// its header with argument names and defaults, and its RETURNS clause.
func signature(definition string) string {
	header, rest, _ := strings.Cut(definition, "\n")
	_, rest, _ = strings.Cut(rest, " RETURNS ")
	returns, _, _ := strings.Cut(rest, "\n")

	return header + "\n" + returns
}

// appendsColumns reports whether the desired view keeps every current column
// with its name and type and only adds columns after them, as CREATE OR
// REPLACE VIEW requires.
func appendsColumns(current, desired introspect.Rendering, view string) bool {
	before := columnsOf(current, view)
	after := columnsOf(desired, view)

	if len(after) < len(before) {
		return false
	}

	for index := range before {
		if before[index].Key != after[index].Key || before[index].Type != after[index].Type {
			return false
		}
	}

	return true
}

// columnsOf is the columns of a table or view in attribute order.
func columnsOf(rendering introspect.Rendering, relation string) []introspect.Column {
	var result []introspect.Column

	for index := range rendering.Columns {
		if strings.HasPrefix(rendering.Columns[index].Key, relation+".") {
			result = append(result, rendering.Columns[index])
		}
	}

	return result
}

func alteredRLS(key, name, after string) Change {
	enable := "DISABLE ROW LEVEL SECURITY"
	kind := DisableRLS

	if strings.Contains(after, "ENABLE ROW LEVEL SECURITY") {
		enable, kind = "ENABLE ROW LEVEL SECURITY", EnableRLS
	}

	force := "NO FORCE ROW LEVEL SECURITY"
	if strings.Contains(after, " FORCE ROW LEVEL SECURITY") {
		force = "FORCE ROW LEVEL SECURITY"
	}

	return Change{
		Kind: kind, Key: key, Table: name, After: after,
		SQL: "ALTER TABLE " + name + " " + enable + ", " + force + ";",
	}
}

// alteredExtension moves an extension to its desired schema and version.
func alteredExtension(key, name, after string) Change {
	_, rest, _ := strings.Cut(after, " WITH SCHEMA ")
	schema, version, _ := strings.Cut(strings.TrimSuffix(rest, ";"), " VERSION ")

	return Change{
		Kind: AlterExtension, Key: key, After: after,
		SQL: "ALTER EXTENSION " + name + " SET SCHEMA " + schema + ";\nALTER EXTENSION " + name +
			" UPDATE TO " + version + ";",
	}
}

// alterSequence turns a rendered CREATE SEQUENCE into the ALTER SEQUENCE
// setting the same options; an absent CYCLE is NO CYCLE, an absent owner
// NONE.
func alterSequence(create string) string {
	statement := strings.Replace(strings.TrimSuffix(create, ";"), "CREATE SEQUENCE ", "ALTER SEQUENCE ", 1)
	options, owner, owned := strings.Cut(statement, " OWNED BY ")

	if !strings.HasSuffix(options, " CYCLE") {
		options += " NO CYCLE"
	}

	if !owned {
		owner = "NONE"
	}

	return options + " OWNED BY " + owner + ";"
}
