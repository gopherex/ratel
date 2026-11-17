// Package objectid spells the canonical identity of a catalog object: the
// diagnostic and lookup key every package agrees on, overloads included.
package objectid

import (
	"strings"

	"github.com/gopherex/ratel/pkg/ir"
)

// ID is the canonical diagnostic and lookup identity, including overloads.
func ID(reference *ir.ObjectRef) string {
	parts := reference.GetName().GetParts()
	routine := reference.GetKind() == ir.ObjectKind_OBJECT_KIND_FUNCTION ||
		reference.GetKind() == ir.ObjectKind_OBJECT_KIND_PROCEDURE
	signature := ""

	if routine && len(parts) > 0 {
		signature = "(" + parts[len(parts)-1] + ")"
		parts = parts[:len(parts)-1]
	}

	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, quoteIdentifier(part))
	}

	return strings.Join(quoted, ".") + signature
}

func quoteIdentifier(value string) string {
	simple := value != ""

	for index, char := range value {
		letter := char >= 'a' && char <= 'z' || char == '_'
		suffix := index > 0 && (char >= '0' && char <= '9' || char == '$')

		if !letter && !suffix {
			simple = false

			break
		}
	}

	if simple {
		return value
	}

	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
