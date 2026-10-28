package catalog

import (
	"strconv"
	"unicode/utf8"
)

// maxIdentifierBytes is NAMEDATALEN - 1.
const maxIdentifierBytes = 63

// objectName is PostgreSQL's makeObjectName: name1 and name2 lose trailing
// bytes, the longer one first, until "name1_name2_label" fits an identifier.
// The label is never truncated. An empty name2 is left out.
func objectName(name1, name2, label string) string {
	overhead := len(label) + 1
	if name2 != "" {
		overhead++
	}

	first, second := len(name1), len(name2)
	for first+second > maxIdentifierBytes-overhead {
		if first > second {
			first--
		} else {
			second--
		}
	}

	name := clip(name1, first)
	if name2 != "" {
		name += "_" + clip(name2, second)
	}

	return name + "_" + label
}

// chooseName is PostgreSQL's ChooseRelationName and ChooseConstraintName:
// on a collision a counter joins the label, so truncation keeps it.
func chooseName(name1, name2, label string, used func(string) bool) string {
	name := objectName(name1, name2, label)
	for pass := 1; used(name); pass++ {
		name = objectName(name1, name2, label+strconv.Itoa(pass))
	}

	return name
}

// nameAddition is PostgreSQL's ChooseIndexNameAddition: column names joined
// by "_", stopping once the text reaches NAMEDATALEN.
func nameAddition(columns []string) string {
	var addition string

	for _, column := range columns {
		if addition != "" {
			addition += "_"
		}

		addition += clip(column, maxIdentifierBytes)
		if len(addition) > maxIdentifierBytes {
			break
		}
	}

	return addition
}

// clip is pg_mbcliplen: at most limit bytes, never splitting a character.
func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}

	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}

	return value
}
