package ddl

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/options"
)

// option reads a ratel extension from descriptor options. An unset extension
// yields the nil pointer of T, and every getter on it is nil-safe.
func option[T any](opts protoreflect.ProtoMessage, extension protoreflect.ExtensionType) T {
	var zero T

	switch value := proto.GetExtension(opts, extension).(type) {
	case T:
		return value
	default:
		return zero
	}
}

// fileSchema returns the schema of the tables declared in a file.
func fileSchema(file protoreflect.FileDescriptor) string {
	if schema := option[*options.File](file.Options(), options.E_File).GetSchema(); schema != "" {
		return schema
	}

	return defaultSchema
}

// tableName returns the SQL name of a table message.
func tableName(msg protoreflect.MessageDescriptor) string {
	if name := option[*options.Table](msg.Options(), options.E_Table).GetName(); name != "" {
		return name
	}

	return snakeCase(string(msg.Name()))
}

// qualifiedTable returns the schema-qualified SQL name of a table message.
func qualifiedTable(msg protoreflect.MessageDescriptor) string {
	return qualify(fileSchema(msg.ParentFile()), tableName(msg))
}

// columnName returns the SQL name of a field before any embed prefix.
func columnName(field protoreflect.FieldDescriptor, opt *options.Column) string {
	if name := opt.GetName(); name != "" {
		return name
	}

	return snakeCase(string(field.Name()))
}

// pgEnum reports whether an enum is declared as a PostgreSQL enum type.
func pgEnum(enum protoreflect.EnumDescriptor) bool {
	return option[*options.Enum](enum.Options(), options.E_Enum).GetPgEnum()
}

// enumTypeName returns the schema-qualified name of the PostgreSQL enum type
// of an enum.
func enumTypeName(enum protoreflect.EnumDescriptor) string {
	return qualify(fileSchema(enum.ParentFile()), snakeCase(string(enum.Name())))
}
