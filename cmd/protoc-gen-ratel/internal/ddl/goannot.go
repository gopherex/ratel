package ddl

import (
	"path"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/options"
)

// goIdent is a Go identifier as ratel-gen-go reads it: the import path and the
// name joined by the last dot. A package whose name is not the path's last
// element (go_package "example.com/shop/v1;shopv1") is named after ";".
func (r registry) goIdent(ident protogen.GoIdent) string {
	importPath := string(ident.GoImportPath)
	if name := string(r.packages[ident.GoImportPath]); name != "" && name != path.Base(importPath) {
		importPath += ";" + name
	}

	return importPath + "." + ident.GoName
}

// goField renders the @go.field annotation of a value column: the Go field
// path from the message root and the conversion between the field and the
// column, so ratel-gen-go can use the message as the model.
func (r registry) goField(field *protogen.Field, opt *options.Column, goPath string) string {
	annotation := "-- @go.field " + goPath + " " + r.goConversion(field, opt)

	// A proto3 optional scalar or enum is a pointer field.
	desc := field.Desc
	if desc.HasPresence() && !desc.IsList() && desc.Kind() != protoreflect.MessageKind &&
		(field.Oneof == nil || field.Oneof.Desc.IsSynthetic()) {
		annotation += " optional"
	}

	if oneof := field.Oneof; oneof != nil && !oneof.Desc.IsSynthetic() {
		annotation += " oneof=" + r.goIdent(field.GoIdent) + " case=" + string(field.Desc.Name())
	}

	return annotation
}

// goConversion names how a field's value becomes its column's, with the Go
// type the conversion needs.
func (r registry) goConversion(field *protogen.Field, opt *options.Column) string {
	desc := field.Desc

	switch {
	case desc.Kind() == protoreflect.EnumKind:
		return "enum type=" + r.goIdent(field.Enum.GoIdent)
	case desc.IsMap():
		key, value := field.Message.Fields[0], field.Message.Fields[1]
		if value.Message != nil {
			return "messagemap key=" + r.goScalar(key) + " type=" + r.goIdent(value.Message.GoIdent)
		}

		return "map key=" + r.goScalar(key) + " value=" + r.goScalar(value)
	case desc.Kind() == protoreflect.MessageKind:
		return r.messageConversion(field, opt)
	case desc.Kind() == protoreflect.Uint32Kind || desc.Kind() == protoreflect.Fixed32Kind:
		return "uint32"
	case desc.Kind() == protoreflect.Uint64Kind || desc.Kind() == protoreflect.Fixed64Kind:
		return "uint64"
	default:
		return "value"
	}
}

// goScalar is the Go type of a scalar or enum field: a map's key or value.
func (r registry) goScalar(field *protogen.Field) string {
	switch field.Desc.Kind() {
	case protoreflect.EnumKind:
		return r.goIdent(field.Enum.GoIdent)
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

func (r registry) messageConversion(field *protogen.Field, opt *options.Column) string {
	message := field.Message
	name := message.Desc.FullName()

	switch {
	case field.Desc.IsList():
		return "messages type=" + r.goIdent(message.GoIdent)
	case name == "google.protobuf.Timestamp":
		return "timestamp"
	case name == "google.protobuf.Duration":
		return "duration"
	default:
		if _, ok := wrapperKind(name); ok && !opt.GetSerialize() {
			return "wrapper type=" + r.goIdent(message.GoIdent)
		}

		return "message type=" + r.goIdent(message.GoIdent)
	}
}

// goCase renders the @go.case annotation of a oneof's case column.
func goCase(goPath string) string {
	return "-- @go.case " + goPath
}

// goEmbed renders the @go.embed annotation of a table: an embedded message's
// Go field path and type, so ratel-gen-go can allocate it.
func (r registry) goEmbed(field *protogen.Field, goPath string) string {
	return "-- @go.embed " + goPath + " " + r.goIdent(field.Message.GoIdent)
}
