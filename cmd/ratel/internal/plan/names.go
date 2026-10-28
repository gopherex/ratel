package plan

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/pgname"
)

// generated is the name the planner gives a helper object: the last part of
// a rendered name with a suffix, cut to the identifier limit and quoted when
// needed. The suffix keeps it from being a keyword.
func generated(name, suffix string) string {
	return quote(pgname.Clip(unquote(lastPart(name)), pgname.MaxBytes-len(suffix)) + suffix)
}

// lastPart is the last dot-separated part of a rendered name, quotes kept.
func lastPart(name string) string {
	quoted := false

	for index := len(name) - 1; index >= 0; index-- {
		switch {
		case name[index] == '"':
			quoted = !quoted
		case name[index] == '.' && !quoted:
			return name[index+1:]
		default:
		}
	}

	return name
}

// schemaOf is the schema part of a rendered "schema.name".
func schemaOf(name string) string {
	return strings.TrimSuffix(name[:len(name)-len(lastPart(name))], ".")
}

// unquote is a rendered identifier as stored.
func unquote(identifier string) string {
	if inner, ok := strings.CutPrefix(identifier, `"`); ok {
		return strings.ReplaceAll(strings.TrimSuffix(inner, `"`), `""`, `"`)
	}

	return identifier
}

// quote renders a non-keyword identifier as quote_ident does.
func quote(identifier string) string {
	for index, char := range identifier {
		letter := char >= 'a' && char <= 'z' || char == '_'
		suffix := index > 0 && (char >= '0' && char <= '9' || char == '$')

		if !letter && !suffix {
			return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
		}
	}

	return identifier
}
