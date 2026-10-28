package gengo

import (
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
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
	param    string // constructor parameter name
	goType   goType
	pgType   string // qualified element type, for InsertField.Type
	nullable bool
	array    bool
	insert   insertKind
}

func (s *state) tableColumns(table *ir.Table) ([]column, error) {
	columns := make([]column, 0, len(table.GetColumns()))

	for _, value := range table.GetColumns() {
		mapped, err := s.columnType(table.GetName(), value)
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
		writeInsert(file, names, columns)
		writeDescriptor(file, schema.GetName(), object, names, columns, table, relations)

		if err := writeConstraints(file, schema.GetName(), object, names, table); err != nil {
			return err
		}
	}

	return nil
}

// identifiers are a table's generated Go names.
type identifiers struct {
	table, model, insert, descriptor, descriptorType, base, scanner, constructor string
}

// relationDecl is a descriptor relation field: its name, target model and
// NewRel expression.
type relationDecl struct{ name, target, value string }

func tableNames(table string) identifiers {
	model := singular(table)

	return identifiers{
		table: table, model: model, insert: model + "Insert", descriptor: plural(table),
		descriptorType: camel(plural(table)) + "Table", base: camel(plural(table)) + "Base",
		scanner: "scan" + model, constructor: "New" + model + "Insert",
	}
}

func (n identifiers) all() map[string]string {
	source := "table " + n.table

	return map[string]string{
		n.model: source, n.insert: source, n.descriptor: source, n.descriptorType: source,
		n.base: source, n.scanner: source, n.constructor: source,
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

func writeInsert(file *unit, names identifiers, columns []column) {
	var parameters, assignments []string

	file.printf("// %s is the input of an insert into %s. Construct it with %s.\ntype %s struct {\n",
		names.insert, names.table, names.constructor, names.insert)

	for _, value := range columns {
		switch value.insert {
		case insertRequired:
			file.printf("\t%s %s\n", value.param, file.use(value.goType))
			parameters = append(parameters, value.param+" "+file.use(value.goType))
			assignments = append(assignments, value.param+": "+value.param)
		case insertNullable:
			file.printf("\t%s ratel.Null[%s]\n", value.field, file.use(value.goType))
		case insertOptional:
			file.printf("\t%s ratel.Opt[%s]\n", value.field, value.modelType(file))
		case insertOmitted:
		}
	}

	file.printf("\tvalid bool\n}\n\n")
	file.printf("// %s sets the columns without a default.\nfunc %s(%s) %s {\n\treturn %s{%s}\n}\n\n",
		names.constructor, names.constructor, strings.Join(parameters, ", "), names.insert, names.insert,
		strings.Join(append(assignments, "valid: true"), ", "))
}
