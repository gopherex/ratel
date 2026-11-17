package catalog

import (
	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/objectid"
)

func typeSignature(value *ir.TypeRef) string {
	if value.GetKind() == ir.TypeKind_TYPE_KIND_ARRAY {
		return typeSignature(value.GetElement()) + "[]"
	}

	if value.GetUdt() != nil {
		return objectid.ID(value.GetUdt())
	}

	return value.GetPgName()
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
