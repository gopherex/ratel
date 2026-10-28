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

// indexColumns is the column addition PostgreSQL names an unnamed index from.
func indexColumns(index *ir.Index) string {
	columns := make([]string, 0, len(index.GetElements()))
	for _, element := range index.GetElements() {
		columns = append(columns, cmp.Or(element.GetColumn(), expressionColumnName(element.GetExpr())))
	}

	return nameAddition(columns)
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

	// Declaration order, as PostgreSQL numbers duplicates: elements of the
	// CREATE TABLE first, then attached constraints in their sorted order.
	for _, constraint := range unnamed {
		constraint.Name = constraintName(table, constraint, func(name string) bool { return used[name] })
		used[constraint.GetName()] = true
	}
}

// nameDomainConstraints names unnamed domain checks "<domain>_check", as
// PostgreSQL does.
func nameDomainConstraints(domain *ir.DomainType) {
	used := make(map[string]bool)

	for _, constraint := range domain.GetConstraints() {
		used[constraint.GetName()] = constraint.GetName() != ""
	}

	parts := domain.GetName().GetParts()

	for _, constraint := range domain.GetConstraints() {
		if constraint.GetName() == "" {
			constraint.Name = chooseName(parts[len(parts)-1], "", "check", func(name string) bool { return used[name] })
			used[constraint.GetName()] = true
		}
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
