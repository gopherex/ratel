package catalog

import (
	"strings"

	"github.com/gopherex/ratel/ir"
)

// ObjectID is the canonical diagnostic and lookup identity, including overloads.
func ObjectID(reference *ir.ObjectRef) string {
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

func typeSignature(value *ir.TypeRef) string {
	if value.GetKind() == ir.TypeKind_TYPE_KIND_ARRAY {
		return typeSignature(value.GetElement()) + "[]"
	}

	if value.GetUdt() != nil {
		return ObjectID(value.GetUdt())
	}

	return value.GetPgName()
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

func qualify(name *ir.QualifiedName, scope string, kind ir.ObjectKind) {
	expected := 2

	switch kind {
	case ir.ObjectKind_OBJECT_KIND_SCHEMA, ir.ObjectKind_OBJECT_KIND_EXTENSION:
		return
	case ir.ObjectKind_OBJECT_KIND_COLUMN, ir.ObjectKind_OBJECT_KIND_CONSTRAINT, ir.ObjectKind_OBJECT_KIND_TRIGGER,
		ir.ObjectKind_OBJECT_KIND_POLICY, ir.ObjectKind_OBJECT_KIND_FUNCTION, ir.ObjectKind_OBJECT_KIND_PROCEDURE:
		expected++
	default:
	}

	if name != nil && len(name.GetParts()) < expected {
		name.Parts = append([]string{scope}, name.GetParts()...)
	}
}
