package diff

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// alteredType is the changes between two renderings of a user type. Enums
// gain and rename labels and domains change in place; anything else, and a
// domain over another base type, is replaced.
func alteredType(current, desired introspect.Rendering, intents Intents, key, before, after string) []Change {
	const enum = " AS ENUM ("

	name := nameOf(key)

	switch {
	case strings.Contains(before, enum) && strings.Contains(after, enum):
		return alteredEnum(intents, key, name, before, after)
	case strings.HasPrefix(before, "CREATE DOMAIN ") && strings.HasPrefix(after, "CREATE DOMAIN "):
		old, _ := domainOf(current, key)
		domain, _ := domainOf(desired, key)

		if old.Type == domain.Type {
			return alteredDomain(key, name, old, domain)
		}
	default:
	}

	return replaced(key, before, after)
}

func domainOf(rendering introspect.Rendering, key string) (introspect.Domain, bool) {
	index := slices.IndexFunc(rendering.Domains, func(domain introspect.Domain) bool { return domain.Key == key })
	if index < 0 {
		return introspect.Domain{}, false
	}

	return rendering.Domains[index], true
}

// alteredEnum renames the labels the intents name and adds the new ones in
// place. Labels removed or reordered have no statement: the change is
// RemoveEnumValue without SQL.
func alteredEnum(intents Intents, key, name, before, after string) []Change {
	old := enumLabels(before)
	labels := enumLabels(after)

	var changes []Change

	for _, label := range labels {
		from, rename := intents.Renamed["value "+name+" "+label]
		index := slices.Index(old, from)

		if !rename || index < 0 || slices.Contains(old, label) {
			continue
		}

		old[index] = label
		changes = append(changes, Change{
			Kind: RenameEnumValue, Key: "value " + name + " " + label, Before: from, After: label,
			SQL: "ALTER TYPE " + name + " RENAME VALUE " + from + " TO " + label + ";",
		})
	}

	kept := slices.DeleteFunc(slices.Clone(labels), func(label string) bool { return !slices.Contains(old, label) })
	if !slices.Equal(kept, old) {
		return append(changes, Change{Kind: RemoveEnumValue, Key: key, Before: before, After: after})
	}

	for index, label := range labels {
		if slices.Contains(old, label) {
			continue
		}

		// Labels are added in order, so the previous one exists; the first
		// goes before the first old label, if there is one.
		var position string

		switch {
		case index > 0:
			position = " AFTER " + labels[index-1]
		case len(old) > 0:
			position = " BEFORE " + old[0]
		default:
		}

		changes = append(changes, Change{
			Kind: AddEnumValue, Key: "value " + name + " " + label, After: label,
			SQL: "ALTER TYPE " + name + " ADD VALUE " + label + position + ";",
		})
	}

	return changes
}

// enumLabels is the label literals of a rendered CREATE TYPE ... AS ENUM, as
// quote_literal wrote them.
func enumLabels(sql string) []string {
	_, rest, _ := strings.Cut(sql, " AS ENUM (")

	var result []string

	for index := 0; index < len(rest); index++ {
		if rest[index] != '\'' {
			continue
		}

		start := index
		if index > 0 && rest[index-1] == 'E' {
			start--
		}

		// A quote inside a literal is doubled.
		for index++; index < len(rest); index++ {
			if rest[index] != '\'' {
				continue
			}

			if index+1 < len(rest) && rest[index+1] == '\'' {
				index++

				continue
			}

			break
		}

		result = append(result, rest[start:index+1])
	}

	return result
}

// alteredDomain changes a domain's default, NOT NULL and checks in place.
func alteredDomain(key, name string, before, after introspect.Domain) []Change {
	var changes []Change

	add := func(kind Kind, from, to, action string) {
		changes = append(changes, Change{
			Kind: kind, Key: key, Before: from, After: to, SQL: "ALTER DOMAIN " + name + " " + action + ";",
		})
	}

	switch {
	case before.Default == after.Default:
	case after.Default == "":
		add(AlterDomain, before.Default, "", "DROP DEFAULT")
	default:
		add(AlterDomain, before.Default, after.Default, "SET DEFAULT "+after.Default)
	}

	switch {
	case before.NotNull == after.NotNull:
	case after.NotNull:
		add(SetDomainNotNull, "", "NOT NULL", "SET NOT NULL")
	default:
		add(AlterDomain, "NOT NULL", "", "DROP NOT NULL")
	}

	for _, check := range before.Checks {
		if !slices.Contains(after.Checks, check) {
			add(DropDomainConstraint, check.Definition, "", "DROP CONSTRAINT "+check.Name)
		}
	}

	for _, check := range after.Checks {
		if !slices.Contains(before.Checks, check) {
			add(AddDomainCheck, "", check.Definition, "ADD CONSTRAINT "+check.Name+" "+check.Definition)
		}
	}

	return changes
}
