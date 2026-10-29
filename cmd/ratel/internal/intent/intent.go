// Package intent reads the migration annotations of a catalog
// (@ratel.renamed, @ratel.retype, @ratel.backfill, @ratel.allow) into the
// intents the diff and the planner use, keyed by rendering keys.
package intent

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/ident"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

// Intents are the annotations of a catalog for the diff and the planner.
type Intents struct {
	Diff diff.Intents
	Plan plan.Intents
}

// From collects the ratel annotations of every catalog object.
func From(catalog *ir.Catalog, quoter *ident.Quoter) Intents {
	result := Intents{
		Diff: diff.Intents{Renamed: make(map[string]string), Retype: make(map[string]string)},
		Plan: plan.Intents{Allow: make(map[string][]string), Backfill: make(map[string]string)},
	}

	walk(catalog.ProtoReflect(), func(annotation *ir.AnnotationValue) {
		key := keyOf(annotation.GetObject(), quoter)
		if annotation.GetNamespace() != "ratel" || key == "" {
			return
		}

		arguments := make(map[string]string)
		for _, argument := range annotation.GetArgs() {
			arguments[argument.GetKey()] = argument.GetValue()
		}

		switch name := annotation.GetName(); {
		case name == "renamed" && arguments["value"] != "":
			// A label rename is keyed by the label, as the diff keys it.
			_, typeName, _ := strings.Cut(key, " ")
			result.Diff.Renamed["value "+typeName+" "+literal(arguments["value"])] = literal(arguments["from"])
		case name == "renamed":
			result.Diff.Renamed[key] = quoter.Quote(arguments["from"])
		case name == "retype":
			result.Diff.Retype[key] = arguments["using"]
		case name == "backfill":
			result.Plan.Backfill[key] = arguments["expr"]
		case name == "allow":
			result.Plan.Allow[key] = append(result.Plan.Allow[key], arguments[""])
		default:
		}
	})

	result.Plan.Retype = result.Diff.Retype

	for key, codes := range result.Plan.Allow {
		slices.Sort(codes)
		result.Plan.Allow[key] = slices.Compact(codes)
	}

	return result
}

// literal renders a label as quote_literal does, which is how introspection
// renders enum labels: quotes doubled, and an E string when it has a
// backslash.
func literal(label string) string {
	quoted := "'" + strings.ReplaceAll(label, "'", "''") + "'"
	if strings.Contains(label, `\`) {
		return "E" + strings.ReplaceAll(quoted, `\`, `\\`)
	}

	return quoted
}

// walk calls visit for every annotation in a message tree.
func walk(message protoreflect.Message, visit func(*ir.AnnotationValue)) {
	switch annotation := message.Interface().(type) {
	case *ir.AnnotationValue:
		visit(annotation)

		return
	default:
	}

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.Message() == nil || field.IsMap():
		case field.IsList():
			for index := range value.List().Len() {
				walk(value.List().Get(index).Message(), visit)
			}
		default:
			walk(value.Message(), visit)
		}

		return true
	})
}

// keyOf is the rendering key of an annotated object, or empty for an object
// the migration engine does not key.
func keyOf(object *ir.ObjectRef, quoter *ident.Quoter) string {
	parts := object.GetName().GetParts()

	switch object.GetKind() {
	case ir.ObjectKind_OBJECT_KIND_TABLE:
		return "table " + quoter.Qualified(parts...)
	case ir.ObjectKind_OBJECT_KIND_COLUMN:
		return "column " + quoter.Qualified(parts...)
	case ir.ObjectKind_OBJECT_KIND_CONSTRAINT:
		return "constraint " + quoter.Qualified(parts...)
	case ir.ObjectKind_OBJECT_KIND_INDEX:
		return "index " + quoter.Qualified(parts...)
	case ir.ObjectKind_OBJECT_KIND_ENUM_TYPE, ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE,
		ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, ir.ObjectKind_OBJECT_KIND_RANGE_TYPE:
		return "type " + quoter.Qualified(parts...)
	case ir.ObjectKind_OBJECT_KIND_VIEW, ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW:
		return "view " + quoter.Qualified(parts...)
	case ir.ObjectKind_OBJECT_KIND_SEQUENCE:
		return "sequence " + quoter.Qualified(parts...)
	default:
		return ""
	}
}
