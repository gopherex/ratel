package catalog

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/ir"
)

func normalize(message protoreflect.Message, path string) {
	fields := message.Descriptor().Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		if field.Name() == "node_id" {
			message.Set(field, protoreflect.ValueOfString(path))

			continue
		}

		if !field.IsList() || field.Message() == nil || !message.Has(field) {
			continue
		}

		switch field.Name() {
		case "schemas", "extensions", "tables", "views", "materialized_views", "sequences", "functions", "procedures",
			"triggers", "enums", "domains", "composites", "ranges", "grants", "constraints", "indexes", "policies":
			list := message.Get(field).List()
			values := make([]protoreflect.Value, 0, list.Len())

			for item := range list.Len() {
				values = append(values, list.Get(item))
			}

			slices.SortStableFunc(values, func(left, right protoreflect.Value) int {
				return cmp.Compare(sortKey(left.Message()), sortKey(right.Message()))
			})

			for item, value := range values {
				list.Set(item, value)
			}
		default:
		}
	}

	_ = children(message, func(child protoreflect.Message, suffix string) error {
		normalize(child, path+"/"+suffix)

		return nil
	})
}

func attachmentKey(value attachment) string {
	priority := "0"

	switch message := value.message.(type) {
	case *ir.Index:
		if message.GetName() == "" {
			priority = "1"
		}
	default:
	}

	return priority + ObjectID(&ir.ObjectRef{Name: value.table}) + semanticKey(value.message)
}

func indexDefaultName(table *ir.Table, index *ir.Index) string {
	parts := []string{table.GetName().GetParts()[1]}

	for _, element := range index.GetElements() {
		name := element.GetColumn()
		if name == "" {
			name = "expr"

			function := element.GetExpr().GetFunctionCall().GetName().GetParts()
			if len(function) > 0 {
				name = function[len(function)-1]
			}
		}

		parts = append(parts, name)
	}

	return strings.Join(append(parts, "idx"), "_")
}

func nameConstraints(table *ir.Table) {
	used := make(map[string]bool)

	var unnamed []*ir.Constraint

	for _, constraint := range table.GetConstraints() {
		if constraint.GetName() == "" {
			unnamed = append(unnamed, constraint)
		} else {
			used[constraint.GetName()] = true
		}
	}

	slices.SortStableFunc(unnamed, func(left, right *ir.Constraint) int {
		return cmp.Compare(semanticKey(left), semanticKey(right))
	})

	for _, constraint := range unnamed {
		base := constraintName(table, constraint)
		name := base

		for suffix := 1; used[name]; suffix++ {
			name = base + strconv.Itoa(suffix)
		}

		constraint.Name = name
		used[name] = true
	}
}

func semanticKey(message proto.Message) string {
	clone := proto.Clone(message)
	clearSource(clone.ProtoReflect())
	normalize(clone.ProtoReflect(), "object")

	data, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(clone)

	return string(data)
}

func clearSource(message protoreflect.Message) {
	fields := message.Descriptor().Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		if field.Message() != nil && field.Message().FullName() == "ratel.v1.ir.SourceSpan" {
			message.Clear(field)
		}
	}

	_ = children(message, func(child protoreflect.Message, _ string) error {
		clearSource(child)

		return nil
	})
}

func sortKey(message protoreflect.Message) string {
	switch value := message.Interface().(type) {
	case *ir.ObjectRef:
		return ObjectID(value)
	case *ir.Grant:
		roles, privileges := slices.Clone(value.GetRoles()), slices.Clone(value.GetPrivileges())
		slices.Sort(roles)
		slices.Sort(privileges)

		value.Roles = roles
		value.Privileges = privileges

		return objectKey(value.GetObject()) + "/" + strings.Join(roles, ",") + "/" +
			strings.Join(privileges, ",") + "/" + strconv.FormatBool(value.GetWithGrantOption())
	default:
	}

	field := message.Descriptor().Fields().ByName("name")
	if field == nil {
		return ""
	}

	if field.Kind() == protoreflect.StringKind {
		return message.Get(field).String()
	}

	switch name := message.Get(field).Message().Interface().(type) {
	case *ir.QualifiedName:
		kind := ir.ObjectKind_OBJECT_KIND_UNSPECIFIED
		switch message.Interface().(type) {
		case *ir.Function:
			kind = ir.ObjectKind_OBJECT_KIND_FUNCTION
		case *ir.Procedure:
			kind = ir.ObjectKind_OBJECT_KIND_PROCEDURE
		default:
		}

		return ObjectID(&ir.ObjectRef{Kind: kind, Name: name})
	default:
		return ""
	}
}
