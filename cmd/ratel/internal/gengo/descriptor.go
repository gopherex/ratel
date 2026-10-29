package gengo

import (
	"go/token"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/pkg/ir"
)

// parameterName is an unexported identifier that is never a Go keyword.
func parameterName(name string) string {
	identifier := camel(name)
	if token.IsKeyword(identifier) || identifier == "" {
		return identifier + "Value"
	}

	return identifier
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}

	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// writeDescriptor writes the table's base WriteTable and its descriptor.
// Relations reference the base of their target, so mutually related tables
// form no initialization cycle.
func (s *state) writeDescriptor(file *unit, schema, table string, names identifiers, columns []column,
	source *ir.Table, relations []relationDecl,
) {
	outputs := make([]string, 0, len(columns))
	for _, value := range columns {
		outputs = append(outputs, value.name)
	}

	// A table with a primary key embeds KeyedTable, which Save and DeleteRow
	// take; a table without one cannot address a row by identity.
	embedded, wrap, unwrap := "WriteTable", "", ""
	if len(primaryKey(source)) > 0 {
		embedded, wrap, unwrap = "KeyedTable", "ratel.NewKeyedTable(", ")"
	}

	file.printf("var %s = %sratel.NewWriteTable(\n", names.base, wrap)
	file.printf("\tratel.NewTable(%s, %s, %s, %s%s),\n", strconv.Quote(schema), strconv.Quote(table),
		quotedList(outputs), names.scanner, tableOptions(names, columns, source))
	file.printf("\tfunc(insert %s) bool { return insert.valid },\n", names.insert)

	for _, value := range columns {
		if value.insert != insertOmitted {
			writeInsertField(file, names, value)
		}
	}

	// The descriptor is a pointer: the writes and sources take it as an
	// interface, which then holds the pointer instead of a copy.
	file.printf(")%s\n\n// %s describes %s.%s.\nvar %s = &%s{\n\t%s: %s,\n", unwrap, names.descriptor,
		schema, table, names.descriptor, names.descriptorType, embedded, names.base)

	fieldTypes := make([]string, len(columns))

	for index, value := range columns {
		constructor, fieldType := s.descriptorColumn(file, names.model, value)
		fieldTypes[index] = fieldType

		file.printf("\t%s: %s,\n", value.field, constructor)
	}

	for _, value := range relations {
		file.printf("\t%s: %s,\n", value.name, value.value)
	}

	file.printf("}\n\ntype %s struct {\n\tratel.%s[%s, %s]\n", names.descriptorType, embedded, names.model,
		names.insert)

	for index, value := range columns {
		file.printf("\t%s %s\n", value.field, fieldTypes[index])
	}

	for _, value := range relations {
		file.printf("\t%s ratel.Rel[%s, %s]\n", value.name, names.model, value.target)
	}

	file.printf("}\n\n// InSchema is the descriptor for a copy of %s in another schema.\n", table)
	file.printf("func (t *%s) InSchema(schema string) *%s {\n\tcopied := *t\n\tcopied.%s = t.%s.InSchema(schema)\n\n"+
		"\treturn &copied\n}\n\n", names.descriptorType, names.descriptorType, embedded, embedded)
}

// columnKind is the constructor of a descriptor column ("NewCol" or
// "NewNullCol"); without its "New" prefix it is the column type.
func columnKind(value column) string {
	if value.nullable {
		return "NewNullCol"
	}

	return "NewCol"
}

func tableOptions(names identifiers, columns []column, table *ir.Table) string {
	var options strings.Builder

	for _, constraint := range table.GetConstraints() {
		key := constraint.GetPrimaryKey().GetColumns()
		if len(key) == 0 {
			continue
		}

		fields := make([]string, 0, len(key))

		for _, name := range key {
			for _, value := range columns {
				if value.name == name {
					fields = append(fields, value.read("model"))
				}
			}
		}

		options.WriteString(",\n\t\t\tratel.PrimaryKey(" + quotedList(key) + ", func(model " + names.model +
			") []any { return []any{" + strings.Join(fields, ", ") + "} })")
	}

	if len(table.GetConstraints()) > 0 {
		options.WriteString(",\n\t\t\tratel.Constraints[" + names.model + "](" + constraintList(names, table) + ")")
	}

	values := make([]string, 0, len(columns))

	for _, value := range columns {
		values = append(values, value.arg("model"))
	}

	options.WriteString(",\n\t\t\tratel.ModelValues(func(model " + names.model + ") []any { return []any{" +
		strings.Join(values, ", ") + "} })")

	return options.String()
}

// setParameter is the parameter of a Set function: named only when the
// result reads it.
func setParameter(insert, set string) string {
	if set == "true" {
		return insert
	}

	return "insert " + insert
}

func writeInsertField(file *unit, names identifiers, value column) {
	access := "insert." + value.param
	set := "true"

	switch value.insert {
	case insertOptional:
		set = access + ".Set"
		access += ".V"
	case insertRequired, insertNullable, insertOmitted:
	}

	file.printf("\t\tratel.InsertField[%s]{\n\t\t\tName: %s, Type: %s%s,\n", names.insert, strconv.Quote(value.name),
		pgTypeName(value.typ), baseField(value))

	argument := access
	if value.storedNull() {
		argument += ".Arg()"
	}

	file.printf("\t\t\tValue: func(insert %s) (any, bool) { return %s, %s },\n", names.insert, argument, set)
	file.printf("\t\t\tSet: func(%s) bool { return %s },\n", setParameter(names.insert, set), set)

	elementType, projected, typed := value.arrayElement(file)
	if !typed {
		file.printf("\t\t},\n")

		return
	}

	element, statement := projection(projected, elementType, access)

	file.printf("\t\t\tValues: func(rows []%s) any {\n\t\t\t\tout := make([]%s, len(rows))\n"+
		"\t\t\t\tfor index, insert := range rows {\n\t\t\t\t\t%s\n\t\t\t\t}\n\n"+
		"\t\t\t\treturn out\n\t\t\t},\n\t\t},\n", names.insert, element, statement)
}
