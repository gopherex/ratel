package gengo

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
)

const (
	ratelpbImport   = "github.com/gopherex/ratel/ratelpb"
	timestampImport = "google.golang.org/protobuf/types/known/timestamppb"
	durationImport  = "google.golang.org/protobuf/types/known/durationpb"
	protoImport     = "google.golang.org/protobuf/proto"

	numericStored = "pgtype.Numeric"
	int64Type     = "int64"
	wrapperUInt32 = "UInt32"
	wrapperUInt64 = "UInt64"
)

// protoColumn is how a proto model moves one column between its message and
// the database (docs/proto-frontend.md, Go models).
type protoColumn struct {
	field     protoField
	oneof     string // a case column's oneof Go path; empty for a field column
	fieldType string // Go type of the field value: the argument of setters and Row steps
	base      string // Go type of the database value
	null      bool   // stored as ratel.Null[base]
	lazy      bool   // stored as any that pgx encodes one by one: no typed array
	read      string // generated func(*Message) stored
	store     string // generated func(fieldType) stored; empty for a case column
	extra     string // statements a member setter runs after storing its value
	embedded  string // the model's field holding the message
}

// stored is the Go type the insert keeps and the read function returns.
func (p *protoColumn) stored() string {
	switch {
	case p.lazy:
		return "any"
	case p.null:
		return "ratel.Null[" + p.base + "]"
	default:
		return p.base
	}
}

// zero is the stored value of an absent field: SQL NULL.
func (p *protoColumn) zero() string {
	switch {
	case p.lazy || strings.HasPrefix(p.base, "[]"):
		return "nil"
	default:
		return p.stored() + "{}"
	}
}

// protoModelOf is the proto model of a table, or nil.
func (s *state) protoModelOf(table *ir.Table) *protoModel {
	return s.protos[strings.Join(table.GetName().GetParts(), ".")]
}

// messageField is the field of the model that embeds the message.
func messageField(message string) string { return message[strings.LastIndex(message, ".")+1:] }

// protoColumns attaches the conversions of a proto model to its columns.
func (s *state) protoColumns(file *unit, names identifiers, model *protoModel, columns []column) {
	prefix := camel(names.descriptor)
	embedded := messageField(file.use(model.message))

	for index := range columns {
		value := &columns[index]
		value.wire = nil // the conversion decides what pgx receives

		column := &protoColumn{read: prefix + "Read" + value.field, embedded: embedded}
		if path, isCase := model.cases[value.name]; isCase {
			column.oneof, column.base, column.null = path, stringType, true
		} else {
			column.field, column.store = model.fields[value.name], prefix+"Store"+value.field
			s.protoTypes(file, *value, column)
		}

		value.proto = column
	}

	for index := range columns {
		if column := columns[index].proto; column.field.oneof.expr != "" {
			column.extra = memberExtra(columns, columns[index])
		}
	}
}

// protoTypes sets the field and database types of a field column.
func (s *state) protoTypes(file *unit, value column, column *protoColumn) {
	field := column.field
	nullable := value.nullable

	switch field.conversion {
	case convValue:
		column.fieldType, column.base, column.null = file.use(value.goType), file.use(value.goType), nullable
	case convUint32:
		column.fieldType, column.base, column.null = "uint32", int64Type, nullable
	case convUint64:
		file.imports = append(file.imports, pgtypeImport)
		column.fieldType, column.base = "uint64", numericStored
	case convEnum:
		column.fieldType, column.base, column.null = file.use(field.typ), stringType, nullable
		if value.array {
			column.fieldType, column.base, column.null = "[]"+column.fieldType, "[]string", false
		}
	case convTimestamp:
		file.imports = append(file.imports, timestampImport, "time")
		column.fieldType, column.base, column.null = "*timestamppb.Timestamp", "time.Time", true
	case convWrapper:
		column.fieldType = "*" + file.use(field.typ)
		column.base, column.null = wrapperBase(file, value, field)
	default:
		column.fieldType, column.lazy = s.lazyType(file, field), true
	}
}

// lazyType is the field type of a conversion stored as any.
func (s *state) lazyType(file *unit, field protoField) string {
	switch field.conversion {
	case convDuration:
		file.imports = append(file.imports, durationImport)

		return "*durationpb.Duration"
	case convMessages:
		return "[]*" + file.use(field.typ)
	case convMap:
		return "map[" + file.use(field.key) + "]" + file.use(field.value)
	case convMessageMap:
		return "map[" + file.use(field.key) + "]*" + file.use(field.typ)
	default:
		return "*" + file.use(field.typ)
	}
}

// wrapperBase is the database value of a wrapper message's column.
func wrapperBase(file *unit, value column, field protoField) (string, bool) {
	switch wrapperKind(field) {
	case wrapperUInt64:
		file.imports = append(file.imports, pgtypeImport)

		return numericStored, false
	case wrapperUInt32:
		return int64Type, true
	default:
		return file.use(value.goType), true
	}
}

// wrapperKind is a wrapper's constructor name in wrapperspb (Int32Value -> Int32).
func wrapperKind(field protoField) string {
	return strings.TrimSuffix(messageField(field.typ.expr), "Value")
}

// conversion renders how a present field value becomes its database value.
func (p *protoColumn) conversion(value string) string {
	switch p.field.conversion {
	case convUint32:
		return "int64(" + value + ")"
	case convUint64:
		return "ratelpb.Numeric(" + value + ")"
	case convEnum:
		if strings.HasPrefix(p.fieldType, "[]") {
			return "ratelpb.EnumNames(" + value + ")"
		}

		return value + ".String()"
	case convTimestamp:
		return value + ".AsTime()"
	case convWrapper:
		return p.wrapperConversion(value)
	case convDuration:
		return "ratelpb.Interval(" + value + ")"
	case convMessage:
		return "ratelpb.JSON(" + value + ")"
	case convMessages:
		return "ratelpb.JSONList(" + value + ")"
	case convMap:
		return "ratelpb.ScalarMap(" + value + ")"
	case convMessageMap:
		return "ratelpb.JSONMap(" + value + ")"
	default:
		return value
	}
}

// wrapperConversion renders the database value of a present wrapper message.
func (p *protoColumn) wrapperConversion(value string) string {
	switch wrapperKind(p.field) {
	case wrapperUInt64:
		return "ratelpb.Numeric(" + value + ".GetValue())"
	case wrapperUInt32:
		return "int64(" + value + ".GetValue())"
	default:
		return value + ".GetValue()"
	}
}

// emptySlice reports a NOT NULL column of a slice value (an array, bytea):
// proto3 holds an empty one as nil, which pgx would send as SQL NULL.
func (p *protoColumn) emptySlice() bool {
	return p.field.conversion == convValue && !p.null && strings.HasPrefix(p.base, "[]")
}

// pointerField reports a field whose Go value is a pointer that can be nil.
func (p *protoColumn) pointerField() bool {
	switch p.field.conversion {
	case convTimestamp, convWrapper:
		return true
	default:
		return false
	}
}

// writeProtoFunctions writes the store and read functions of the columns.
func writeProtoFunctions(file *unit, model *protoModel, columns []column) {
	file.imports = append(file.imports, ratelpbImport)
	message := file.use(model.message)

	for _, value := range columns {
		column := value.proto
		if column.store != "" {
			writeStore(file, value, column)
		}

		file.printf("// %s is the value of the column %s in a message; absent is NULL.\n"+
			"func %s(m *%s) %s {\n", column.read, value.name, column.read, message, column.stored())

		parent := navigate(file, column)
		file.printf("%s}\n\n", readLeaf(file, columns, column, parent))
	}
}

func writeStore(file *unit, value column, column *protoColumn) {
	file.printf("// %s is the stored value of the column %s.\nfunc %s(v %s) %s {\n", column.store, value.name,
		column.store, column.fieldType, column.stored())

	converted := column.conversion("v")

	switch {
	case column.emptySlice():
		file.printf("\tif v == nil {\n\t\treturn %s{}\n\t}\n\n\treturn %s\n}\n\n", column.base, converted)
	case column.lazy:
		file.printf("\treturn %s\n}\n\n", converted)
	case column.pointerField():
		file.printf("\tif v == nil {\n\t\treturn %s\n\t}\n\n\treturn %s\n}\n\n", column.zero(), wrapNull(column, converted))
	default:
		file.printf("\treturn %s\n}\n\n", wrapNull(column, converted))
	}
}

func wrapNull(column *protoColumn, value string) string {
	if column.null {
		return "ratel.NullFrom(" + value + ")"
	}

	return value
}

// segments splits a column's Go path into the embedded messages leading to
// it, and its leaf: a field, or a oneof and its member.
func segments(column *protoColumn) ([]string, string, string) {
	path := column.oneof
	if path == "" {
		path = column.field.path
	}

	parts := strings.Split(path, ".")

	switch {
	case column.oneof != "":
		return parts[:len(parts)-1], parts[len(parts)-1], ""
	case column.field.oneof.expr != "":
		return parts[:len(parts)-2], parts[len(parts)-2], parts[len(parts)-1]
	default:
		return parts[:len(parts)-1], "", parts[len(parts)-1]
	}
}

// navigate writes the getters down to the column's parent message, returning
// NULL when one is absent; it returns the parent's variable.
func navigate(file *unit, column *protoColumn) string {
	embeds, _, _ := segments(column)
	parent := "m"

	for index, segment := range embeds {
		next := "p" + strconv.Itoa(index)
		file.printf("\t%s := %s.Get%s()\n\tif %s == nil {\n\t\treturn %s\n\t}\n\n", next, parent, segment, next,
			column.zero())

		parent = next
	}

	return parent
}

// readLeaf renders the statements that read the column from its parent.
func readLeaf(file *unit, columns []column, column *protoColumn, parent string) string {
	_, oneof, leaf := segments(column)

	switch {
	case column.oneof != "":
		var cases strings.Builder

		for _, member := range columns {
			if member.proto.field.oneof.expr != "" && memberOf(member.proto) == column.oneof {
				cases.WriteString("\tcase *" + file.use(member.proto.field.oneof) + ":\n\t\treturn ratel.NullFrom(" +
					strconv.Quote(member.proto.field.caseName) + ")\n")
			}
		}

		return "\tswitch " + parent + ".Get" + oneof + "().(type) {\n" + cases.String() +
			"\tdefault:\n\t\treturn " + column.zero() + "\n\t}\n"
	case column.field.oneof.expr != "":
		return "\tswitch value := " + parent + ".Get" + oneof + "().(type) {\n\tcase *" + file.use(column.field.oneof) +
			":\n\t\treturn " + column.store + "(value." + leaf + ")\n\tdefault:\n\t\treturn " + column.zero() + "\n\t}\n"
	case column.field.optional:
		return "\tif " + parent + "." + leaf + " == nil {\n\t\treturn " + column.zero() + "\n\t}\n\n\treturn " +
			column.store + "(*" + parent + "." + leaf + ")\n"
	default:
		return "\treturn " + column.store + "(" + parent + "." + leaf + ")\n"
	}
}

// memberOf is the oneof path of a member column.
func memberOf(column *protoColumn) string {
	return column.field.path[:strings.LastIndex(column.field.path, ".")]
}

// memberExtra sets a member's case column and clears the oneof's other
// members, so the row holds exactly one.
func memberExtra(columns []column, member column) string {
	oneof := memberOf(member.proto)

	var extra strings.Builder

	for _, other := range columns {
		switch {
		case other.proto.oneof == oneof:
			extra.WriteString("\tr." + other.param + " = ratel.NullFrom(" + strconv.Quote(member.proto.field.caseName) + ")\n")
		case other.name != member.name && other.proto.field.oneof.expr != "" && memberOf(other.proto) == oneof:
			extra.WriteString("\tr." + other.param + " = " + other.proto.zero() + "\n")
		}
	}

	return extra.String()
}

// writeProtoModel writes the model: the message and the loaded relations.
func writeProtoModel(file *unit, names identifiers, model *protoModel, related bool) {
	message := file.use(model.message)

	file.printf("// %s is a row of %s: its proto message and loaded relations.\ntype %s struct {\n\t*%s\n",
		names.model, names.table, names.model, message)

	if related {
		file.printf("\tR %s\n", relationsType(names.model))
	}

	file.printf("}\n\n")
}

// protoOnlyEnum reports an enum that only proto models use: their columns
// convert it to the proto enum, so its Go type would be dead code. Any other
// use (a table, view, composite, domain or query) keeps it.
func (s *state) protoOnlyEnum(name *ir.QualifiedName) bool {
	key := schemaOf(name) + "." + objectOf(name)
	proto := false

	for _, schema := range s.catalog.GetSchemas() {
		for _, table := range schema.GetTables() {
			for _, value := range table.GetColumns() {
				if !s.refers(value.GetType(), key) {
					continue
				}

				if s.protoModelOf(table) == nil {
					return false
				}

				proto = true
			}
		}

		if s.otherSchemaUse(schema, key) {
			return false
		}
	}

	for _, query := range s.queries {
		if s.queryUse(query, key) {
			return false
		}
	}

	return proto
}

// otherSchemaUse reports a use of a type by a view, composite or domain.
func (s *state) otherSchemaUse(schema *ir.Schema, key string) bool {
	var types []*ir.TypeRef

	for _, view := range schema.GetViews() {
		for _, value := range view.GetOutput() {
			types = append(types, value.GetType())
		}
	}

	for _, view := range schema.GetMaterializedViews() {
		for _, value := range view.GetOutput() {
			types = append(types, value.GetType())
		}
	}

	for _, composite := range schema.GetComposites() {
		for _, field := range composite.GetFields() {
			types = append(types, field.GetType())
		}
	}

	for _, domain := range schema.GetDomains() {
		types = append(types, domain.GetBaseType())
	}

	return slices.ContainsFunc(types, func(typ *ir.TypeRef) bool { return s.refers(typ, key) })
}

// queryUse reports a use of a type by a query's parameters or columns.
func (s *state) queryUse(query *ir.Query, key string) bool {
	for _, parameter := range query.GetParameters() {
		if s.refers(parameter.GetType(), key) {
			return true
		}
	}

	for _, value := range query.GetColumns() {
		if s.refers(value.GetType(), key) {
			return true
		}
	}

	return false
}

// refers reports whether a type is the named user type, an array of it, or a
// domain over it.
func (s *state) refers(typ *ir.TypeRef, key string) bool {
	for typ.GetElement() != nil {
		typ = typ.GetElement()
	}

	if !isUserType(typ) {
		return false
	}

	name := pgName(typ)
	if name == key {
		return true
	}

	found, ok := s.types[name]

	return ok && found.kind == ir.TypeKind_TYPE_KIND_DOMAIN && s.refers(found.base, key)
}

// checkProtoKeys rejects a relation key column whose conversion has no typed
// database value: keys travel as typed arrays.
func checkProtoKeys(names identifiers, columns []column, edges []edge) error {
	for _, value := range edges {
		for _, name := range value.local {
			for _, local := range columns {
				if local.name == name && local.proto != nil && (local.proto.lazy || local.proto.base == numericStored) {
					return &plugin.Error{Code: codeAnnotation, Message: "relation " + value.name + " of table " +
						names.table + " has the key column " + name + ", whose proto conversion cannot be a key"}
				}
			}
		}
	}

	return nil
}
