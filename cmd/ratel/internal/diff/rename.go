package diff

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// renameTables returns the renames of tables and types the intents name and
// current as it is after them: the object and every statement and key
// naming it carry the new name, so the rest of the diff compares like with
// like.
func renameTables(current, desired introspect.Rendering, intents Intents) ([]Change, introspect.Rendering) {
	before := statements(current)

	var renames []Change

	for _, object := range desired.Objects {
		kind, name := kindOf(object.Key), nameOf(object.Key)
		old, rename := intents.Renamed[object.Key]

		if kind != tableObject && kind != typeObject || !rename {
			continue
		}

		schema, target, _ := strings.Cut(name, ".")
		from := schema + "." + old

		previous, existed := before[kind.key(from)]
		if _, exists := before[object.Key]; exists || !existed {
			continue
		}

		change := Change{
			Kind: RenameTable, Key: object.Key, Table: from, Before: kind.key(from), After: object.Key,
			SQL: "ALTER TABLE " + from + " RENAME TO " + target + ";",
		}
		if kind == typeObject {
			change.Kind, change.Table = RenameType, ""
			change.SQL = "ALTER " + typeKeyword(previous) + " " + from + " RENAME TO " + target + ";"
		}

		renames = append(renames, change)
		current = Requalified(current, from, name)
	}

	return renames, current
}

// Requalified is the rendering with the qualified name from replaced by
// target, as it is after renaming a table.
func Requalified(rendering introspect.Rendering, from, target string) introspect.Rendering {
	result := introspect.Rendering{SQL: rendering.SQL, Major: rendering.Major, Database: rendering.Database}

	for index := range rendering.Domains {
		domain := rendering.Domains[index]
		domain.Key = requalify(domain.Key, from, target)
		domain.Type = requalify(domain.Type, from, target)
		domain.Default = requalify(domain.Default, from, target)
		result.Domains = append(result.Domains, domain)
	}

	for _, object := range rendering.Objects {
		result.Objects = append(result.Objects, introspect.Object{
			Key: requalify(object.Key, from, target), SQL: requalify(object.SQL, from, target),
		})
	}

	for index := range rendering.Columns {
		column := rendering.Columns[index]
		for _, part := range []*string{
			&column.Key, &column.Definition, &column.Type, &column.Default, &column.Generated,
		} {
			*part = requalify(*part, from, target)
		}

		result.Columns = append(result.Columns, column)
	}

	return result
}

// requalify replaces each occurrence of the qualified name from that stands
// alone, not inside a longer identifier.
func requalify(text, from, target string) string {
	var out strings.Builder

	for {
		index := strings.Index(text, from)
		if index < 0 {
			out.WriteString(text)

			return out.String()
		}

		end := index + len(from)
		alone := (index == 0 || !nameByte(text[index-1])) && (end == len(text) || !identifierByte(text[end]))

		out.WriteString(text[:index])

		if alone {
			out.WriteString(target)
		} else {
			out.WriteString(from)
		}

		text = text[end:]
	}
}

func identifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// nameByte is a byte that may precede a schema name inside a longer name.
func nameByte(b byte) bool {
	return identifierByte(b) || b == '.' || b == '"'
}

// renamedObjects turns a dropped index or constraint and a created one that
// differ only in name into a rename.
func renamedObjects(changes, drops []Change) []Change {
	for index := range changes {
		create := &changes[index]

		if kind := kindOf(create.Key); kind != indexObject && kind != constraintObject || create.Before != "" {
			continue
		}

		match := slices.IndexFunc(drops, func(drop Change) bool {
			return drop.Kind == dropOf(create.Kind) && unnamed(&drop) == unnamed(create)
		})
		if match < 0 {
			continue
		}

		*create = renamed(&drops[match], create)
		drops = slices.Delete(drops, match, match+1)
	}

	return append(changes, drops...)
}

func dropOf(kind Kind) Kind {
	if kind == CreateIndex {
		return DropIndex
	}

	return DropConstraint
}

// unnamed is the definition of an index or constraint change without its
// name.
func unnamed(change *Change) string {
	kind, name := kindOf(change.Key), nameOf(change.Key)
	definition := change.Before + change.After
	name = name[strings.LastIndex(name, ".")+1:]

	if kind == indexObject {
		return strings.Replace(definition, " INDEX "+name+" ON ", " INDEX ON ", 1)
	}

	return strings.Replace(definition, " ADD CONSTRAINT "+name+" ", " ADD CONSTRAINT ", 1)
}

func renamed(drop, create *Change) Change {
	from, target := nameOf(drop.Key), nameOf(create.Key)
	target = target[strings.LastIndex(target, ".")+1:]

	change := Change{Key: create.Key, Table: create.Table, Before: drop.Before, After: create.After}

	if create.Kind == CreateIndex {
		change.Kind, change.SQL = RenameIndex, "ALTER INDEX "+from+" RENAME TO "+target+";"

		return change
	}

	change.Kind = RenameConstraint
	change.SQL = "ALTER TABLE " + create.Table + " RENAME CONSTRAINT " + child(from) + " TO " + target + ";"

	return change
}
