package gengo

import (
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
)

// insertKind is how a column appears in the insert type.
type insertKind uint8

const (
	insertRequired insertKind = iota // NOT NULL, no default: constructor argument
	insertNullable                   // nullable, no default: ratel.Null, zero inserts NULL
	insertOptional                   // default or BY DEFAULT identity: ratel.Opt, omitted unless set
	insertOmitted                    // generated or ALWAYS identity: PostgreSQL computes it
)

type column struct {
	typ      *ir.TypeRef
	name     string // PostgreSQL name
	field    string // Go field name
	param    string // unexported field of the insert type
	goType   goType
	pgType   string // qualified element type, for InsertField.Type
	nullable bool
	array    bool
	insert   insertKind
}

func (s *state) tableColumns(table *ir.Table) ([]column, error) {
	columns := make([]column, 0, len(table.GetColumns()))

	for _, value := range table.GetColumns() {
		mapped, err := s.columnType(table.GetName(), value, schemaOf(table.GetName()))
		if err != nil {
			return nil, err
		}

		columns = append(columns, column{
			typ: value.GetType(), name: value.GetName(), field: pascal(value.GetName()),
			param:  parameterName(value.GetName()),
			goType: mapped, pgType: pgTypeName(value.GetType()), nullable: value.GetNullable(),
			array: value.GetType().GetElement() != nil, insert: insertKindOf(value),
		})
	}

	return columns, nil
}

func insertKindOf(value *ir.Column) insertKind {
	switch {
	case value.GetGenerated() != nil || value.GetIdentity().GetKind() == ir.IdentityKind_IDENTITY_KIND_ALWAYS:
		return insertOmitted
	case value.GetDefaultExpr() != nil || value.GetIdentity() != nil:
		return insertOptional
	case value.GetNullable():
		return insertNullable
	default:
		return insertRequired
	}
}

// pgTypeName is the schema-qualified element type, as Go source of a
// pgx.Identifier.
func pgTypeName(typ *ir.TypeRef) string {
	for typ.GetElement() != nil {
		typ = typ.GetElement()
	}

	if typ.GetUdt() != nil {
		name := typ.GetUdt().GetName()

		return "pgx.Identifier{" + strconv.Quote(schemaOf(name)) + ", " + strconv.Quote(objectOf(name)) + "}"
	}

	return "pgx.Identifier{\"pg_catalog\", " + strconv.Quote(typ.GetPgName()) + "}"
}

func (c column) modelType(file *unit) string {
	if c.nullable {
		return "ratel.Null[" + file.use(c.goType) + "]"
	}

	return file.use(c.goType)
}

func (s *state) tables(file *unit, schema *ir.Schema) error {
	for _, table := range schema.GetTables() {
		if table.GetPartitionOf() != nil {
			continue // partitions are reached through their parent
		}

		columns, err := s.tableColumns(table)
		if err != nil {
			return err
		}

		object := objectOf(table.GetName())
		names := tableNames(object)

		for identifier, source := range names.all() {
			if err := file.declare(identifier, source); err != nil {
				return err
			}
		}

		file.imports = append(file.imports, ratelImport, "github.com/jackc/pgx/v5")

		if err := s.checkEnums(file, names, table, columns); err != nil {
			return err
		}

		for _, value := range columns {
			s.use(file, value.typ, value.goType, value.nullable || value.insert == insertOptional)
		}

		edges := s.edges(table)
		if err := checkEdges(names, columns, edges); err != nil {
			return err
		}

		relations := make([]relationDecl, 0, len(edges))
		for _, value := range edges {
			relations = append(relations, relationDecl{
				name: value.name, target: s.targetModel(file, value.target),
				value: s.relationField(file, names, columns, value),
			})
		}

		writeModel(file, names, columns, len(edges) > 0)

		if len(edges) > 0 {
			if err := s.writeRelations(file, names, edges); err != nil {
				return err
			}
		}

		writeScanner(file, names, columns)

		if err := writeInsert(file, names, columns); err != nil {
			return err
		}

		writeDescriptor(file, schema.GetName(), object, names, columns, table, relations)

		if err := writeConstraints(file, schema.GetName(), object, names, table); err != nil {
			return err
		}
	}

	return nil
}

// checkColumns rejects columns whose Go names would clash with the generated
// Row method of the descriptor or the flag of the insert type.
func checkColumns(names identifiers, columns []column) error {
	for _, value := range columns {
		switch {
		case value.field == "Row":
			return &plugin.Error{Code: codeCollision, Message: "column " + value.name + " of table " + names.table +
				" collides with the generated method Row of " + names.descriptor}
		case value.param == "valid" && value.insert != insertOmitted:
			return &plugin.Error{Code: codeCollision, Message: "column " + value.name + " of table " + names.table +
				" collides with the validity flag of " + names.insert}
		}
	}

	return nil
}

// identifiers are a table's generated Go names.
type identifiers struct {
	table, model, insert, descriptor, descriptorType, base, scanner string
}

// relationDecl is a descriptor relation field: its name, target model and
// NewRel expression.
type relationDecl struct{ name, target, value string }

func tableNames(table string) identifiers {
	model := singular(table)

	return identifiers{
		table: table, model: model, insert: model + "Insert", descriptor: plural(table),
		descriptorType: camel(plural(table)) + "Table", base: camel(plural(table)) + "Base",
		scanner: "scan" + model,
	}
}

func (n identifiers) all() map[string]string {
	source := "table " + n.table

	return map[string]string{
		n.model: source, n.insert: source, n.descriptor: source, n.descriptorType: source,
		n.base: source, n.scanner: source,
	}
}

func writeModel(file *unit, names identifiers, columns []column, related bool) {
	file.printf("// %s is a row of %s.\ntype %s struct {\n", names.model, names.table, names.model)

	for _, value := range columns {
		file.printf("\t%s %s\n", value.field, value.modelType(file))
	}

	if related {
		file.printf("\tR %s\n", relationsType(names.model))
	}

	file.printf("}\n\n")
}

func writeScanner(file *unit, names identifiers, columns []column) {
	targets := make([]string, 0, len(columns))
	for _, value := range columns {
		targets = append(targets, "&model."+value.field)
	}

	file.printf("func %s(row pgx.Row) (%s, error) {\n\tvar model %s\n\n"+
		"\terr := row.Scan(%s)\n\n\treturn model, err\n}\n\n",
		names.scanner, names.model, names.model, strings.Join(targets, ", "))
}

// writeInsert writes the insert type, its setters for optional columns, and
// Row: one step type per required column, in column order, each with one
// method naming its column. Only the last step returns the insert type, which
// carries the flag the writes check.
func writeInsert(file *unit, names identifiers, columns []column) error {
	var required []column

	file.printf("// %s is the input of an insert into %s. Start it with Row.\ntype %s struct {\n",
		names.insert, names.table, names.insert)

	for _, value := range columns {
		switch value.insert {
		case insertRequired:
			required = append(required, value)
			file.printf("\t%s %s\n", value.param, file.use(value.goType))
		case insertNullable:
			file.printf("\t%s ratel.Null[%s]\n", value.param, file.use(value.goType))
		case insertOptional:
			file.printf("\t%s ratel.Opt[%s]\n", value.param, value.modelType(file))
		case insertOmitted:
		}
	}

	file.printf("\tvalid bool\n}\n\n")

	for _, value := range columns {
		switch value.insert {
		case insertNullable:
			file.printf("// %s sets the column %s.\nfunc (r %s) %s(v ratel.Null[%s]) %s {\n\tr.%s = v\n\n\treturn r\n}\n\n",
				value.field, value.name, names.insert, value.field, file.use(value.goType), names.insert, value.param)
		case insertOptional:
			file.printf("// %s sets the column %s; unset, its default applies.\n"+
				"func (r %s) %s(v %s) %s {\n\tr.%s = ratel.OptFrom(v)\n\n\treturn r\n}\n\n",
				value.field, value.name, names.insert, value.field, value.modelType(file), names.insert, value.param)
		case insertRequired, insertOmitted:
		}
	}

	return writeRow(file, names, required)
}

func writeRow(file *unit, names identifiers, required []column) error {
	if len(required) == 0 {
		file.printf("// Row starts an insert row of %s; it has no required column.\n"+
			"func (t *%s) Row() %s {\n\treturn %s{valid: true}\n}\n\n",
			names.table, names.descriptorType, names.insert, names.insert)

		return nil
	}

	steps := make([]string, len(required))

	for index, value := range required {
		steps[index] = camel(names.model) + "Row" + value.field
		if err := file.declare(steps[index], "insert step of table "+names.table); err != nil {
			return err
		}
	}

	file.printf("// Row starts an insert row of %s: one step per required column, in column order.\n"+
		"func (t *%s) Row() %s {\n\treturn %s{}\n}\n\n", names.table, names.descriptorType, steps[0], steps[0])

	for index, value := range required {
		file.printf("type %s struct{ insert %s }\n\n", steps[index], names.insert)

		result, last := steps[min(index+1, len(steps)-1)], index == len(required)-1
		if last {
			result = names.insert
		}

		file.printf("// %s sets the column %s.\nfunc (s %s) %s(v %s) %s {\n\ts.insert.%s = v\n",
			value.field, value.name, steps[index], value.field, file.use(value.goType), result, value.param)

		switch {
		case last:
			file.printf("\ts.insert.valid = true\n\n\treturn s.insert\n}\n\n")
		default:
			file.printf("\n\treturn %s{s.insert}\n}\n\n", result)
		}
	}

	return nil
}
