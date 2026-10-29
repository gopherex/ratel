package gengo

import (
	"strconv"
	"strings"
)

// read is the Go expression of the column's database value in a model held
// by model: a field of an SQL model, the read function of a proto model.
func (c column) read(model string) string {
	if c.proto == nil {
		return model + "." + c.field
	}

	return c.proto.read + "(" + model + "." + c.proto.embedded + ")"
}

// arg is the column's value in a model as pgx receives it.
func (c column) arg(model string) string {
	if c.storedNull() {
		return c.read(model) + ".Arg()"
	}

	return c.read(model)
}

// storedNull reports a column whose insert and read values are ratel.Null.
func (c column) storedNull() bool {
	if c.proto == nil {
		return c.nullable
	}

	return c.proto.null
}

// insertType is the Go type of the column in the insert type.
func (c column) insertType(file *unit) string {
	switch {
	case c.insert == insertOptional && c.proto != nil:
		return "ratel.Opt[" + c.proto.stored() + "]"
	case c.insert == insertOptional:
		return "ratel.Opt[" + c.modelType(file) + "]"
	case c.proto != nil:
		return c.proto.stored()
	case c.insert == insertNullable:
		return "ratel.Null[" + file.use(c.goType) + "]"
	default:
		return file.use(c.goType)
	}
}

// setterType is the argument type of the column's Row step or setter.
func (c column) setterType(file *unit) string {
	switch {
	case c.proto != nil:
		return c.proto.fieldType
	case c.insert == insertOptional:
		return c.modelType(file)
	case c.insert == insertNullable:
		return "ratel.Null[" + file.use(c.goType) + "]"
	default:
		return file.use(c.goType)
	}
}

// stored is the insert value of a setter argument.
func (c column) stored(argument string) string {
	if c.proto == nil {
		return argument
	}

	return c.proto.store + "(" + argument + ")"
}

// setter reports whether the insert type has a setter for the column: a
// proto case column is set by its members.
func (c column) setter() bool {
	return c.proto == nil || c.proto.oneof == ""
}

// arrayElement is the element type of the column's typed array for unnest,
// with the column whose nullability and wire the projection follows; ok is
// false for a column without one, which inserts through VALUES.
func (c column) arrayElement(file *unit) (string, column, bool) {
	switch {
	case c.array:
		return "", c, false
	case c.proto == nil:
		return file.use(c.goType), c, true
	case c.proto.lazy:
		return "", c, false
	default:
		return c.proto.base, column{nullable: c.proto.null}, true
	}
}

// descriptorColumn renders the constructor of the column's descriptor field
// and the field's type.
func (s *state) descriptorColumn(file *unit, model string, value column) (string, string) {
	kind := columnKind(value)
	if value.proto != nil && value.proto.field.oneof.expr != "" {
		kind = "NewNullCol"
	}

	if value.proto == nil || !value.proto.codec() {
		typ := file.use(value.goType)
		if value.proto != nil && value.proto.oneof != "" {
			typ = stringType
		}

		return "ratel." + kind + "[" + model + ", " + typ + "](" + strconv.Quote(value.name) + ")",
			"ratel." + kind[3:] + "[" + model + ", " + typ + "]"
	}

	typ := value.proto.fieldType
	kind = strings.Replace(kind, "New", "NewCodec", 1)

	return "ratel." + kind + "[" + model + "](" + strconv.Quote(value.name) + ", " + s.codecLiteral(file, value) + ")",
		"ratel." + strings.TrimPrefix(kind, "NewCodec") + "[" + model + ", " + typ + "]"
}

// codec reports a column whose Go values convert on the way to pgx.
func (p *protoColumn) codec() bool {
	switch p.field.conversion {
	case convUint32, convUint64, convEnum, convTimestamp, convDuration:
		return p.oneof == ""
	default:
		return false
	}
}

// encode renders the pgx argument of a field value v of a codec column.
func (p *protoColumn) encode() string {
	switch p.field.conversion {
	case convTimestamp:
		return "ratelpb.Time(v)"
	default:
		return p.conversion("v")
	}
}

// codecLiteral renders the ratel.Codec of a converting column.
func (s *state) codecLiteral(file *unit, value column) string {
	column := value.proto
	nullable := value
	nullable.nullable = true

	slot := s.scanSlot(file, nullable, "scanned")
	empty := "ratel.Null[" + column.fieldType + "]{}"

	check := "err != nil"
	if slot.valid != "" {
		check += " || !(" + slot.valid + ")"
	}

	return "ratel.Codec[" + column.fieldType + "]{\n\t\tEncode: func(v " + column.fieldType + ") any { return " +
		column.encode() + " },\n\t\tScan: func(rows pgx.Rows) (" + empty[:len(empty)-2] + ", error) {\n" +
		"\t\t\tvar scanned " + slot.temp + "\n\t\t\tif err := rows.Scan(&scanned); " + check + " {\n\t\t\t\treturn " +
		empty + ", err\n\t\t\t}\n\n" + indent(loadValue(file, column, slot.raw, empty)) +
		"\n\t\t\treturn ratel.NullFrom(value), nil\n\t\t},\n\t}"
}

// writeRowOf writes RowOf: an insert row with every insertable column from a
// message. A column with a default or identity keeps it while its field holds
// the zero value, since proto3 cannot tell an unset field from a zero one.
func writeRowOf(file *unit, names identifiers, model *protoModel, columns []column) {
	message := file.use(model.message)

	file.printf("// RowOf is an insert row of %s with every insertable column from the message; a\n"+
		"// column with a default keeps it while its field holds the zero value.\n"+
		"func (t *%s) RowOf(m *%s) %s {\n\tif m == nil {\n\t\tm = &%s{}\n\t}\n\n\treturn %s{\n",
		names.table, names.descriptorType, message, names.insert, message, names.insert)

	for _, value := range columns {
		read := value.proto.read + "(m)"

		switch value.insert {
		case insertOptional:
			file.printf("\t\t%s: ratel.Opt[%s]{V: %s, Set: %s},\n", value.param, value.proto.stored(), read,
				value.proto.present(read, file))
		case insertRequired, insertNullable:
			file.printf("\t\t%s: %s,\n", value.param, read)
		case insertOmitted:
		}
	}

	file.printf("\t\tvalid: true,\n\t}\n}\n\n")
}

// present renders whether a column's field holds a value other than its
// zero value, given the column's read value.
func (p *protoColumn) present(read string, file *unit) string {
	switch {
	case p.lazy:
		return read + " != nil"
	case p.null, p.base == numericStored:
		return read + ".Valid"
	case strings.HasPrefix(p.base, "[]"):
		return "len(" + read + ") > 0"
	case p.base == boolType:
		return read
	case p.base == stringType && p.field.conversion == convEnum:
		return read + " != " + file.use(p.field.typ) + "(0).String()"
	case p.base == stringType:
		return read + " != \"\""
	default:
		return read + " != 0"
	}
}
