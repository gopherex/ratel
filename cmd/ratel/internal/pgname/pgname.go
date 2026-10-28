// Package pgname chooses object names the way PostgreSQL does, so ratel
// knows the name PostgreSQL gives a constraint or index it creates.
package pgname

import (
	"strconv"
	"unicode/utf8"
)

// MaxBytes is NAMEDATALEN - 1.
const MaxBytes = 63

// Object is PostgreSQL's makeObjectName: name1 and name2 lose trailing
// bytes, the longer one first, until "name1_name2_label" fits an identifier.
// The label is never truncated. An empty name2 is left out.
func Object(name1, name2, label string) string {
	overhead := len(label) + 1
	if name2 != "" {
		overhead++
	}

	first, second := len(name1), len(name2)
	for first+second > MaxBytes-overhead {
		if first > second {
			first--
		} else {
			second--
		}
	}

	name := Clip(name1, first)
	if name2 != "" {
		name += "_" + Clip(name2, second)
	}

	return name + "_" + label
}

// Choose is PostgreSQL's ChooseRelationName and ChooseConstraintName: on a
// collision a counter joins the label, so truncation keeps it.
func Choose(name1, name2, label string, used func(string) bool) string {
	name := Object(name1, name2, label)
	for pass := 1; used(name); pass++ {
		name = Object(name1, name2, label+strconv.Itoa(pass))
	}

	return name
}

// Addition is PostgreSQL's ChooseIndexNameAddition: column names joined by
// "_", stopping once the text reaches NAMEDATALEN.
func Addition(columns []string) string {
	var addition string

	for _, column := range columns {
		if addition != "" {
			addition += "_"
		}

		addition += Clip(column, MaxBytes)
		if len(addition) > MaxBytes {
			break
		}
	}

	return addition
}

// Clip is pg_mbcliplen: at most limit bytes, never splitting a character.
func Clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}

	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}

	return value
}
