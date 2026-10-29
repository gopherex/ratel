package gengo

import (
	"slices"
	"strconv"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
)

// checkEdges rejects a column that collides with generated members, and a
// relation name that collides with a model field, the relations field R or
// another relation of the same table.
func checkEdges(names identifiers, columns []column, edges []edge) error {
	if err := checkColumns(names, columns); err != nil {
		return err
	}

	taken := map[string]string{"R": "the relations field R"}
	for _, value := range columns {
		taken[value.field] = "column " + value.name
	}

	for _, value := range edges {
		identifiers := []string{value.name}
		if value.many {
			identifiers = append(identifiers, countField(value))
		}

		for _, identifier := range identifiers {
			if previous, exists := taken[identifier]; exists {
				return &plugin.Error{Code: codeCollision, Message: "relation " + value.name + " of table " + names.table +
					" collides with " + previous + "; rename it with @ratel.rel name=..."}
			}

			taken[identifier] = "relation " + value.name
		}
	}

	return nil
}

// edge is one relation as seen from a table: its own key columns, the target
// table and the target-side columns they match. A many-to-many edge goes
// through a join table: keys match the join table's columns toward this
// table, and targetColumns are its columns toward the target's key.
type edge struct {
	name          string
	many          bool
	target        *ir.Table
	local         []string // this table's columns
	remote        []string // target, or join table, columns matching local
	through       *ir.Table
	targetColumns []string // join table columns matching the target's primary key
}

func (s *state) table(name *ir.QualifiedName) *ir.Table {
	for _, schema := range s.catalog.GetSchemas() {
		for _, table := range schema.GetTables() {
			if slices.Equal(table.GetName().GetParts(), name.GetParts()) {
				return table
			}
		}
	}

	return nil
}

func primaryKey(table *ir.Table) []string {
	for _, constraint := range table.GetConstraints() {
		if key := constraint.GetPrimaryKey().GetColumns(); len(key) > 0 {
			return key
		}
	}

	return nil
}

// edges lists the relations a table loads. A target without a primary key
// takes no relation. A relation into another schema exists only on its
// foreign-key side: the inverse would make the two packages import each other.
func (s *state) edges(table *ir.Table) []edge {
	var out []edge

	for _, relation := range s.catalog.GetRelations() {
		source := s.table(relation.GetFrom().GetTable().GetName())
		destination := s.table(relation.GetTo().GetTable().GetName())

		if source == nil || destination == nil {
			continue
		}

		through := relation.GetThrough()

		if source == table && relation.GetName() != "" {
			out = s.appendEdge(out, table, destination, relation.GetName(), relation.GetKind(),
				relation.GetFrom().GetColumns(), relation.GetTo().GetColumns(), through, relation, false)
		}

		sameSchema := schemaOf(source.GetName()) == schemaOf(table.GetName())
		if destination == table && relation.GetInverseName() != "" && (through != nil || sameSchema) {
			out = s.appendEdge(out, table, source, relation.GetInverseName(), relation.GetInverseKind(),
				relation.GetTo().GetColumns(), relation.GetFrom().GetColumns(), through, relation, true)
		}
	}

	return out
}

func (s *state) appendEdge(out []edge, table, target *ir.Table, name string, kind ir.RelationKind,
	local, remote []string, through *ir.ObjectRef, relation *ir.Relation, inverse bool,
) []edge {
	if len(primaryKey(target)) == 0 {
		return out
	}

	value := edge{
		name: name, many: kind == ir.RelationKind_RELATION_KIND_MANY, target: target, local: local, remote: remote,
	}

	if through != nil {
		join := s.table(through.GetName())
		if join == nil || schemaOf(join.GetName()) != schemaOf(table.GetName()) ||
			schemaOf(target.GetName()) != schemaOf(table.GetName()) {
			return out
		}

		keys := joinKeys(join, relation, inverse)
		value.through, value.remote, value.targetColumns = join, keys.self, keys.target
	}

	return append(out, value)
}

// joinColumns are a join table's columns toward this side and toward the
// target, each in the order of the key it references.
type joinColumns struct{ self, target []string }

func joinKeys(join *ir.Table, relation *ir.Relation, inverse bool) joinColumns {
	var first, second []string

	for _, constraint := range join.GetConstraints() {
		key := constraint.GetForeignKey()
		if key == nil {
			continue
		}

		if constraint.GetName() == objectOf(relation.GetFk().GetName()) {
			first = key.GetColumns()
		} else {
			second = key.GetColumns()
		}
	}

	if inverse {
		return joinColumns{self: second, target: first}
	}

	return joinColumns{self: first, target: second}
}

func relationsType(model string) string { return model + "Relations" }

func countField(edge edge) string { return inflection.Singular(edge.name) + "Count" }

// targetModel is the target's model type as named from the package schema.
func (s *state) targetModel(file *unit, target *ir.Table) string {
	model := singular(objectOf(target.GetName()))

	if owner := schemaOf(target.GetName()); owner != file.schema {
		file.imports = append(file.imports, s.importPath(owner))

		return packageName(owner) + "." + model
	}

	return model
}

func (s *state) targetDescriptor(file *unit, target *ir.Table) string {
	if owner := schemaOf(target.GetName()); owner != file.schema {
		return packageName(owner) + "." + plural(objectOf(target.GetName()))
	}

	return camel(plural(objectOf(target.GetName()))) + "Base"
}

func (s *state) writeRelations(file *unit, names identifiers, edges []edge) error {
	if err := file.declare(relationsType(names.model), "relations of table "+names.table); err != nil {
		return err
	}

	file.printf("// %s holds the loaded relations of a %s.\ntype %s struct {\n", relationsType(names.model),
		names.model, relationsType(names.model))

	for _, value := range edges {
		file.printf("\t%s ratel.Loaded[%s]\n", value.name, s.loadedType(file, value))

		if value.many {
			file.printf("\t%s ratel.Loaded[int64]\n", countField(value))
		}
	}

	file.printf("}\n\n")

	for _, value := range edges {
		file.printf("// %s is the loaded %s relation; ratel.ErrNotLoaded unless requested.\n"+
			"func (m *%s) %s() (%s, error) { return m.R.%s.Get() }\n\n", value.name, value.name, names.model,
			value.name, s.loadedType(file, value), value.name)
	}

	return nil
}

func (s *state) loadedType(file *unit, value edge) string {
	if value.many {
		return "[]" + s.targetModel(file, value.target)
	}

	return "*" + s.targetModel(file, value.target)
}

// relationField renders the descriptor's NewRel for an edge.
func (s *state) relationField(file *unit, names identifiers, columns []column, value edge) string {
	target := s.targetModel(file, value.target)

	keys := make([]string, 0, len(value.local))

	for index, name := range value.local {
		for _, local := range columns {
			if local.name == name {
				keys = append(keys, relKey(names.model, local, value.remote[index]))
			}
		}
	}

	cardinality, attach := "ratel.RelOne", "var value *"+target+"\n\t\t\tif len(children) > 0 {\n"+
		"\t\t\t\tvalue = &children[0]\n\t\t\t}\n\n\t\t\tmodel.R."+value.name+" = ratel.LoadedFrom(value)"
	if value.many {
		cardinality, attach = "ratel.RelMany", "model.R."+value.name+" = ratel.LoadedFrom(children)"
	}

	var options strings.Builder

	if value.many {
		options.WriteString(",\n\t\tratel.RelCount[" + names.model + ", " + target + "](func(model *" + names.model +
			", count int64) { model.R." + countField(value) + " = ratel.LoadedFrom(count) })")
	}

	if value.through != nil {
		options.WriteString(",\n\t\tratel.Through[" + names.model + ", " + target + "](" +
			strconv.Quote(schemaOf(value.through.GetName())) + ", " + strconv.Quote(objectOf(value.through.GetName())))

		for _, name := range value.targetColumns {
			options.WriteString(", " + strconv.Quote(name))
		}

		options.WriteString(")")
	}

	return "ratel.NewRel[" + names.model + ", " + target + "](" + s.targetDescriptor(file, value.target) + ", " +
		cardinality + ",\n\t\t[]ratel.RelKey[" + names.model + "]{" + strings.Join(keys, ", ") + "},\n" +
		"\t\tfunc(model *" + names.model + ", children []" + target + ") {\n\t\t\t" + attach + "\n\t\t}" +
		options.String() + ")"
}

// relKey projects one local key column into a native array.
func relKey(model string, local column, remote string) string {
	elementType, projected := local.goType.expr, local
	if local.proto != nil {
		elementType, projected = local.proto.base, column{nullable: local.proto.null}
	}

	element, statement := projection(projected, elementType, local.read("model"))

	return "{\n\t\t\tColumn: " + strconv.Quote(remote) + ", Parent: " + strconv.Quote(local.name) +
		", Type: " + pgTypeName(local.typ) + baseField(local) + ",\n" +
		"\t\t\tValues: func(models []" + model + ") any {\n\t\t\t\tout := make([]" + element + ", len(models))\n" +
		"\t\t\t\tfor index, model := range models {\n\t\t\t\t\t" + statement + "\n\t\t\t\t}\n\n" +
		"\t\t\t\treturn out\n\t\t\t},\n\t\t}"
}
