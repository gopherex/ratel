package gengo

import (
	"go/token"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
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

func writeDescriptor(file *unit, schema, table string, names identifiers, columns []column, source *ir.Table) {
	outputs := make([]string, 0, len(columns))
	for _, value := range columns {
		outputs = append(outputs, value.name)
	}

	file.printf("// %s describes %s.%s.\nvar %s = %s{\n\tWriteTable: ratel.NewWriteTable(\n", names.descriptor,
		schema, table, names.descriptor, names.descriptorType)
	file.printf("\t\tratel.NewTable(%s, %s, %s, %s%s),\n", strconv.Quote(schema), strconv.Quote(table),
		quotedList(outputs), names.scanner, tableOptions(names, columns, source))
	file.printf("\t\tfunc(insert %s) bool { return insert.valid },\n", names.insert)

	for _, value := range columns {
		if value.insert != insertOmitted {
			writeInsertField(file, names, value)
		}
	}

	file.printf("\t),\n")

	for _, value := range columns {
		file.printf("\t%s: ratel.%s[%s, %s](%s),\n", value.field, columnKind(value), names.model,
			file.use(value.goType), strconv.Quote(value.name))
	}

	file.printf("}\n\ntype %s struct {\n\tratel.WriteTable[%s, %s]\n", names.descriptorType, names.model, names.insert)

	for _, value := range columns {
		file.printf("\t%s ratel.%s[%s, %s]\n", value.field, columnKind(value)[3:], names.model, file.use(value.goType))
	}

	file.printf("}\n\n// InSchema is the descriptor for a copy of %s in another schema.\n", table)
	file.printf("func (t %s) InSchema(schema string) %s {\n\tt.WriteTable = t.WriteTable.InSchema(schema)\n\n"+
		"\treturn t\n}\n\n", names.descriptorType, names.descriptorType)
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
					fields = append(fields, "model."+value.field)
				}
			}
		}

		options.WriteString(",\n\t\t\tratel.PrimaryKey(" + quotedList(key) + ", func(model " + names.model +
			") []any { return []any{" + strings.Join(fields, ", ") + "} })")
	}

	if len(table.GetConstraints()) > 0 {
		options.WriteString(",\n\t\t\tratel.Constraints[" + names.model + "](" + constraintList(names, table) + ")")
	}

	return options.String()
}

func writeInsertField(file *unit, names identifiers, value column) {
	access := "insert." + value.field
	set := "true"

	switch value.insert {
	case insertRequired:
		access = "insert." + parameterName(value.name)
	case insertOptional:
		set = access + ".Set"
		access += ".V"
	case insertNullable, insertOmitted:
	}

	file.printf("\t\tratel.InsertField[%s]{\n\t\t\tName: %s, Type: %s,\n", names.insert, strconv.Quote(value.name),
		value.pgType)
	file.printf("\t\t\tValue: func(insert %s) (any, bool) { return %s, %s },\n", names.insert, access, set)

	if value.array {
		file.printf("\t\t\tArray: true,\n\t\t},\n")

		return
	}

	element, project := file.use(value.goType), access
	if value.nullable {
		element, project = "*"+element, access+".Ptr()"
	}

	file.printf("\t\t\tValues: func(rows []%s) any {\n\t\t\t\tout := make([]%s, len(rows))\n"+
		"\t\t\t\tfor index, insert := range rows {\n\t\t\t\t\tout[index] = %s\n\t\t\t\t}\n\n"+
		"\t\t\t\treturn out\n\t\t\t},\n\t\t},\n", names.insert, element, project)
}
