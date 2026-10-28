package ddl

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/options"
)

// jsonbType is the column type of every field stored as a document.
const jsonbType = "jsonb"

// typeSpec is the SQL type of a column and the CHECK expression it needs.
type typeSpec struct {
	typ   string
	check string
}

// columnType returns the SQL type of a value column. quoted is the quoted
// column name used by the CHECK expression.
func columnType(field *protogen.Field, opt *options.Column, quoted string) (typeSpec, error) {
	if field.Desc.Kind() == protoreflect.EnumKind {
		return enumColumn(field, opt, quoted), nil
	}

	if override := opt.GetType(); override != "" {
		return typeSpec{typ: override}, nil
	}

	typ, err := baseType(field, opt)

	return typeSpec{typ: typ}, err
}

// enumColumn maps an enum field to a PostgreSQL enum type, or to text with a
// CHECK over the enum's value names.
func enumColumn(field *protogen.Field, opt *options.Column, quoted string) typeSpec {
	usesPgType := pgEnum(field.Enum.Desc)
	list := field.Desc.IsList()

	var typ string

	switch {
	case opt.GetType() != "":
		typ = opt.GetType()
	case usesPgType:
		typ = enumTypeName(field.Enum.Desc)
	default:
		typ = "text"
	}

	if list && opt.GetType() == "" {
		typ += "[]"
	}

	if usesPgType {
		return typeSpec{typ: typ}
	}

	return typeSpec{typ: typ, check: enumCheck(quoted, field.Enum, list)}
}

// enumCheck restricts a text column, or every element of a text array, to the
// value names of the enum.
func enumCheck(quoted string, enum *protogen.Enum, list bool) string {
	labels := make([]string, 0, len(enum.Values))
	for _, value := range enum.Values {
		labels = append(labels, sqlString(string(value.Desc.Name())))
	}

	if list {
		return quoted + " <@ ARRAY[" + strings.Join(labels, ", ") + "]"
	}

	return quoted + " IN (" + strings.Join(labels, ", ") + ")"
}

// baseType maps a non-enum field to its SQL type, without a type override.
func baseType(field *protogen.Field, opt *options.Column) (string, error) {
	if field.Desc.IsMap() {
		return jsonbType, nil
	}

	if field.Desc.Kind() == protoreflect.MessageKind {
		return messageType(field, opt)
	}

	if opt.GetSerialize() {
		return "", fmt.Errorf("%w: serialize needs a message field", ErrInvalidOption)
	}

	typ, err := scalarType(field.Desc.Kind())
	if err != nil {
		return "", err
	}

	if field.Desc.IsList() {
		typ += "[]"
	}

	return typ, nil
}

// messageType maps a message field: repeated and serialized messages are
// jsonb, well-known types and wrappers have fixed types, and any other
// message must be embedded or serialized explicitly.
func messageType(field *protogen.Field, opt *options.Column) (string, error) {
	name := field.Message.Desc.FullName()

	if field.Desc.IsList() || opt.GetSerialize() {
		return jsonbType, nil
	}

	if typ, ok := wellKnownType(name); ok {
		return typ, nil
	}

	if kind, ok := wrapperKind(name); ok {
		return scalarType(kind)
	}

	return "", fmt.Errorf("%w: %s is a message: use embed, serialize or a well-known type",
		ErrUnsupportedType, name)
}

// scalarType maps a proto scalar kind to its PostgreSQL type.
func scalarType(kind protoreflect.Kind) (string, error) {
	switch kind {
	case protoreflect.BoolKind:
		return "bool", nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int4", nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "int8", nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int8", nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "", fmt.Errorf("%w: uint64 does not fit int8, set Column.type", ErrUnsupportedType)
	case protoreflect.FloatKind:
		return "float4", nil
	case protoreflect.DoubleKind:
		return "float8", nil
	case protoreflect.StringKind:
		return "text", nil
	case protoreflect.BytesKind:
		return "bytea", nil
	default:
		return "", fmt.Errorf("%w: kind %v", ErrUnsupportedType, kind)
	}
}

// wellKnownType maps the well-known messages that have a single PostgreSQL
// type.
func wellKnownType(name protoreflect.FullName) (string, bool) {
	switch name {
	case "google.protobuf.Timestamp":
		return "timestamptz", true
	case "google.protobuf.Duration":
		return "interval", true
	case "google.protobuf.Struct", "google.protobuf.Value",
		"google.protobuf.ListValue", "google.protobuf.Any":
		return jsonbType, true
	default:
		return "", false
	}
}

// wrapperKind returns the scalar kind wrapped by a google.protobuf wrapper.
func wrapperKind(name protoreflect.FullName) (protoreflect.Kind, bool) {
	switch name {
	case "google.protobuf.BoolValue":
		return protoreflect.BoolKind, true
	case "google.protobuf.Int32Value":
		return protoreflect.Int32Kind, true
	case "google.protobuf.UInt32Value":
		return protoreflect.Uint32Kind, true
	case "google.protobuf.Int64Value":
		return protoreflect.Int64Kind, true
	case "google.protobuf.UInt64Value":
		return protoreflect.Uint64Kind, true
	case "google.protobuf.FloatValue":
		return protoreflect.FloatKind, true
	case "google.protobuf.DoubleValue":
		return protoreflect.DoubleKind, true
	case "google.protobuf.StringValue":
		return protoreflect.StringKind, true
	case "google.protobuf.BytesValue":
		return protoreflect.BytesKind, true
	default:
		return 0, false
	}
}
