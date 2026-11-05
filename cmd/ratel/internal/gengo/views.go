package gengo

import (
	"strconv"

	"github.com/gopherex/ratel/pkg/ir"
)

// view generates a model, scanner and read-only descriptor of a view or
// materialized view, from the columns the host inferred.
func (s *state) view(file *unit, name *ir.QualifiedName, output []*ir.QueryColumn, kind string) error {
	schema, object := schemaOf(name), objectOf(name)
	names := tableNames(object)

	for identifier := range names.all() {
		if identifier == names.insert || identifier == names.base {
			continue
		}

		if err := file.declare(identifier, kind+" "+object); err != nil {
			return err
		}
	}

	columns := make([]column, 0, len(output))

	for _, value := range output {
		mapped, err := s.goType(value.GetType(), file.pkg)
		if err != nil {
			return err
		}

		columns = append(columns, column{
			typ: value.GetType(), name: value.GetName(), field: pascal(value.GetName()), goType: mapped,
			nullable: value.GetNullable(),
		})

		s.use(file, value.GetType())
	}

	file.imports = append(file.imports, ratelImport, "github.com/jackc/pgx/v5")
	writeModel(file, names, columns, false)
	s.writeScanner(file, names, columns)

	outputs := make([]string, 0, len(columns))
	for _, value := range columns {
		outputs = append(outputs, value.name)
	}

	file.printf("// %s describes the %s %s.%s; it is read-only.\nvar %s = &%s{\n", names.descriptor, kind, schema,
		object, names.descriptor, names.descriptorType)
	file.printf("\tTable: ratel.NewTable(%s, %s, %s, %s),\n", strconv.Quote(schema), strconv.Quote(object),
		quotedList(outputs), names.scanner)

	for _, value := range columns {
		file.printf("\t%s: ratel.%s[%s, %s](%s),\n", value.field, columnKind(value), names.model,
			file.use(value.goType), strconv.Quote(value.name))
	}

	file.printf("}\n\ntype %s struct {\n\tratel.Table[%s]\n", names.descriptorType, names.model)

	for _, value := range columns {
		file.printf("\t%s ratel.%s[%s, %s]\n", value.field, columnKind(value)[3:], names.model, file.use(value.goType))
	}

	file.printf("}\n\n// InSchema is the descriptor for a copy of %s in another schema.\n", object)
	file.printf("func (t *%s) InSchema(schema string) *%s {\n\tcopied := *t\n"+
		"\tcopied.Table = t.Table.InSchema(schema)\n\n\treturn &copied\n}\n\n", names.descriptorType, names.descriptorType)

	return nil
}
