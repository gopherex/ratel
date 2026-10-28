package gengo

import (
	"strconv"

	"github.com/gopherex/ratel/ir"
)

// views generates a model, scanner and read-only descriptor per view and
// materialized view, from the columns the host inferred.
func (s *state) views(file *unit, schema *ir.Schema) error {
	for _, view := range schema.GetViews() {
		if err := s.view(file, schema.GetName(), view.GetName(), view.GetOutput(), "view"); err != nil {
			return err
		}
	}

	for _, view := range schema.GetMaterializedViews() {
		if err := s.view(file, schema.GetName(), view.GetName(), view.GetOutput(), "materialized view"); err != nil {
			return err
		}
	}

	return nil
}

func (s *state) view(file *unit, schema string, name *ir.QualifiedName, output []*ir.QueryColumn, kind string) error {
	object := objectOf(name)
	names := tableNames(object)

	for identifier := range names.all() {
		if identifier == names.insert || identifier == names.constructor {
			continue
		}

		if err := file.declare(identifier, kind+" "+object); err != nil {
			return err
		}
	}

	columns := make([]column, 0, len(output))

	for _, value := range output {
		mapped, err := s.goType(value.GetType(), schema)
		if err != nil {
			return err
		}

		columns = append(columns, column{
			typ: value.GetType(), name: value.GetName(), field: pascal(value.GetName()), goType: mapped,
			nullable: value.GetNullable(),
		})

		s.use(file, value.GetType(), mapped, value.GetNullable())
	}

	file.imports = append(file.imports, ratelImport, "github.com/jackc/pgx/v5")
	writeModel(file, names, columns)
	writeScanner(file, names, columns)

	outputs := make([]string, 0, len(columns))
	for _, value := range columns {
		outputs = append(outputs, value.name)
	}

	file.printf("// %s describes the %s %s.%s; it is read-only.\nvar %s = %s{\n", names.descriptor, kind, schema,
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
	file.printf("func (t %s) InSchema(schema string) %s {\n\tt.Table = t.Table.InSchema(schema)\n\n"+
		"\treturn t\n}\n\n", names.descriptorType, names.descriptorType)

	return nil
}
