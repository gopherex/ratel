package ddl

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/options"
)

// goIdent is a Go identifier as ratel-gen-go reads it: the import path and the
// name joined by the last dot.
func goIdent(ident protogen.GoIdent) string {
	return string(ident.GoImportPath) + "." + ident.GoName
}

// goField renders the @go.field annotation of a value column: the Go field
// path from the message root and the conversion between the field and the
// column, so ratel-gen-go can use the message as the model.
func goField(field *protogen.Field, opt *options.Column, path string) string {
	annotation := "-- @go.field " + path + " " + goConversion(field, opt)

	if oneof := field.Oneof; oneof != nil && !oneof.Desc.IsSynthetic() {
		annotation += " oneof=" + goIdent(field.GoIdent) + " case=" + string(field.Desc.Name())
	}

	return annotation
}

// goConversion names how a field's value becomes its column's, with the Go
// type the conversion needs.
func goConversion(field *protogen.Field, opt *options.Column) string {
	desc := field.Desc

	switch {
	case desc.Kind() == protoreflect.EnumKind:
		return "enum type=" + goIdent(field.Enum.GoIdent)
	case desc.IsMap():
		key, value := field.Message.Fields[0], field.Message.Fields[1]
		if value.Message != nil {
			return "messagemap key=" + goScalar(key) + " type=" + goIdent(value.Message.GoIdent)
		}

		return "map key=" + goScalar(key) + " value=" + goScalar(value)
	case desc.Kind() == protoreflect.MessageKind:
		return messageConversion(field, opt)
	case desc.Kind() == protoreflect.Uint32Kind || desc.Kind() == protoreflect.Fixed32Kind:
		return "uint32"
	case desc.Kind() == protoreflect.Uint64Kind || desc.Kind() == protoreflect.Fixed64Kind:
		return "uint64"
	case desc.HasPresence() && !desc.IsList() && (field.Oneof == nil || field.Oneof.Desc.IsSynthetic()):
		return "optional"
	default:
		return "value"
	}
}

// goScalar is the Go type of a scalar or enum field: a map's key or value.
func goScalar(field *protogen.Field) string {
	switch field.Desc.Kind() {
	case protoreflect.EnumKind:
		return goIdent(field.Enum.GoIdent)
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.BytesKind:
		return "[]byte"
	default:
		return "string"
	}
}

func messageConversion(field *protogen.Field, opt *options.Column) string {
	message := field.Message
	name := message.Desc.FullName()

	switch {
	case field.Desc.IsList():
		return "messages type=" + goIdent(message.GoIdent)
	case name == "google.protobuf.Timestamp":
		return "timestamp"
	case name == "google.protobuf.Duration":
		return "duration"
	default:
		if _, ok := wrapperKind(name); ok && !opt.GetSerialize() {
			return "wrapper type=" + goIdent(message.GoIdent)
		}

		return "message type=" + goIdent(message.GoIdent)
	}
}

// goCase renders the @go.case annotation of a oneof's case column.
func goCase(path string) string {
	return "-- @go.case " + path
}

// goEmbed renders the @go.embed annotation of a table: an embedded message's
// Go field path and type, so ratel-gen-go can allocate it.
func goEmbed(field *protogen.Field, path string) string {
	return "-- @go.embed " + path + " " + goIdent(field.Message.GoIdent)
}
