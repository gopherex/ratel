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
		annotation += " oneof=" + goIdent(field.GoIdent)
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
		if value := field.Message.Fields[1]; value.Message != nil {
			return "messagemap type=" + goIdent(value.Message.GoIdent)
		}

		return "map"
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
