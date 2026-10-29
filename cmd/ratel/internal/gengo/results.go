package gengo

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
)

// resultColumn is one output column with the Go expression it scans into.
type resultColumn struct {
	typ      *ir.TypeRef
	name     string
	goType   goType
	nullable bool
	field    string // Row field, for a Row with one field per column
	target   string // scan target expression
	null     *nullTarget
}

// shape is a query's Go result type and how each column scans into it.
type shape struct {
	typeName string // Go type of one row
	declare  string // its declaration, when the query defines it
	columns  []resultColumn
	before   string // temporaries declared before Scan
	after    string // statements after a successful Scan, building nullable models
}

// tableGroup is a set of output columns that together are every column of
// one table instance (one table alias).
type tableGroup struct {
	table   *ir.Table
	alias   string
	indexes []int // output positions, in table column order
}

// grouping splits a result into complete table instances and the rest.
type grouping struct {
	full []tableGroup
	rest []int // output positions outside a complete table
}

// columnParts is the length of a source column name: schema, table, column.
const columnParts = 3

func (s *state) groups(value *ir.Query) grouping {
	type key struct{ table, alias string }

	found := make(map[key]*tableGroup)

	var order []key

	for index, column := range value.GetColumns() {
		source := column.GetSourceColumn().GetName().GetParts()
		if len(source) != columnParts {
			continue
		}

		// A proto model's columns are read into its message by the table's
		// scanner only; a query returns them in a Row of database values.
		table := s.table(&ir.QualifiedName{Parts: source[:2]})
		if table == nil || s.protoModelOf(table) != nil {
			continue
		}

		instance := key{table: source[0] + "." + source[1], alias: column.GetTableAlias()}
		if found[instance] == nil {
			found[instance] = &tableGroup{table: table, alias: column.GetTableAlias()}

			order = append(order, instance)
		}

		found[instance].indexes = append(found[instance].indexes, index)
	}

	var result grouping

	covered := make(map[int]bool)

	for _, instance := range order {
		group := found[instance]
		if complete(group, value) {
			result.full = append(result.full, *group)

			for _, index := range group.indexes {
				covered[index] = true
			}
		}
	}

	for index := range value.GetColumns() {
		if !covered[index] {
			result.rest = append(result.rest, index)
		}
	}

	return result
}

// complete reports whether a group holds every table column exactly once.
func complete(group *tableGroup, value *ir.Query) bool {
	if len(group.indexes) != len(group.table.GetColumns()) {
		return false
	}

	seen := make(map[string]bool)

	for _, index := range group.indexes {
		parts := value.GetColumns()[index].GetSourceColumn().GetName().GetParts()
		seen[parts[2]] = true
	}

	for _, column := range group.table.GetColumns() {
		if !seen[column.GetName()] {
			return false
		}
	}

	return true
}

func (s *state) resultShape(file *unit, value *ir.Query) (shape, error) {
	if row := annotationValue(value, "ratel", "row"); row != "" {
		return s.ownShape(file, value, row, true)
	}

	if row := annotationValue(value, "", "row"); row != "" {
		return s.userShape(file, value, row)
	}

	grouped := s.groups(value)
	full, rest := grouped.full, grouped.rest

	switch {
	case len(full) == 1 && len(rest) == 0 && !groupNullable(full[0], value):
		return s.modelShape(file, value, full[0]), nil
	case len(full) == 1 && !groupNullable(full[0], value):
		return s.embeddedShape(file, value, full[0], rest)
	case len(full) > 1 && len(rest) == 0:
		return s.joinedShape(file, value, full)
	default:
		return s.ownShape(file, value, value.GetName()+"Row", false)
	}
}

// annotationValue is the first argument of a query annotation; an empty
// namespace means the plugin's own.
func annotationValue(value *ir.Query, namespace, name string) string {
	for _, annotation := range value.GetMeta().GetAnnotations() {
		own := namespace == "" && annotation.GetNamespace() != "ratel"
		if (own || annotation.GetNamespace() == namespace) && annotation.GetName() == name &&
			len(annotation.GetArgs()) > 0 {
			return annotation.GetArgs()[0].GetValue()
		}
	}

	return ""
}

// groupNullable reports a table instance on the nullable side of an outer
// join: one of its NOT NULL columns is nullable in the result.
func groupNullable(group tableGroup, value *ir.Query) bool {
	for _, index := range group.indexes {
		column := value.GetColumns()[index]
		name := column.GetSourceColumn().GetName().GetParts()[2]

		for _, declared := range group.table.GetColumns() {
			if declared.GetName() == name && !declared.GetNullable() && column.GetNullable() {
				return true
			}
		}
	}

	return false
}

func (s *state) modelType(file *unit, table *ir.Table) string {
	file.imports = append(file.imports, s.importPath(schemaOf(table.GetName())))

	return packageName(schemaOf(table.GetName())) + "." + singular(objectOf(table.GetName()))
}

// modelShape scans every column of one table into its model.
func (s *state) modelShape(file *unit, value *ir.Query, group tableGroup) shape {
	result := shape{typeName: s.modelType(file, group.table)}
	targets := s.groupTargets(file, value, group, "model")

	for index := range value.GetColumns() {
		result.columns = append(result.columns, targets[index])
	}

	return result
}

// groupTargets maps each output position of a table group to the model field
// at prefix it scans into.
func (s *state) groupTargets(file *unit, value *ir.Query, group tableGroup, prefix string) map[int]resultColumn {
	targets := make(map[int]resultColumn, len(group.indexes))

	for _, index := range group.indexes {
		name := value.GetColumns()[index].GetSourceColumn().GetName().GetParts()[2]
		target := resultColumn{name: value.GetColumns()[index].GetName(), target: "&" + prefix + "." + pascal(name)}

		declared := group.table.GetColumns()[slices.IndexFunc(group.table.GetColumns(),
			func(c *ir.Column) bool { return c.GetName() == name })]
		if declared.GetNullable() {
			mapped, _ := s.columnType(group.table.GetName(), declared, "")
			target.null = s.nullTarget(file, declared.GetType(), mapped)
		}

		targets[index] = target
	}

	return targets
}

func (s *state) column(file *unit, value *ir.Query, index int) (resultColumn, error) {
	column := value.GetColumns()[index]

	mapped, err := s.goType(column.GetType(), "")
	if err != nil {
		return resultColumn{}, err
	}

	s.use(file, column.GetType())

	return resultColumn{
		typ: column.GetType(), name: column.GetName(), goType: mapped, nullable: column.GetNullable(),
		field: pascal(column.GetName()),
	}, nil
}

func (c resultColumn) fieldType(file *unit) string {
	if c.nullable {
		file.imports = append(file.imports, ratelImport)

		return "ratel.Null[" + file.use(c.goType) + "]"
	}

	return file.use(c.goType)
}

// ownShape declares a Row with one field per column. A shared @ratel.row
// type is declared once; every query using it must have the same shape.
func (s *state) ownShape(file *unit, value *ir.Query, name string, shared bool) (shape, error) {
	result := shape{typeName: name}

	var declaration strings.Builder

	comment := "// " + name + " is a row of " + value.GetName() + ".\n"
	if shared {
		comment = "// " + name + " is the shared row type of the queries annotated @ratel.row " + name + ".\n"
	}

	declaration.WriteString(comment + "type " + name + " struct {\n")

	seen := make(map[string]bool)

	for index := range value.GetColumns() {
		column, err := s.column(file, value, index)
		if err != nil {
			return shape{}, err
		}

		if seen[column.field] {
			return shape{}, &plugin.Error{
				Code: "RTL-G08", Message: "query " + value.GetName() +
					": two result columns are named " + column.name + "; rename one with AS",
				Source: value.GetMeta().GetSource(),
			}
		}

		seen[column.field] = true
		column.target = "&model." + column.field

		if column.nullable {
			column.null = s.nullTarget(file, column.typ, column.goType)
		}

		result.columns = append(result.columns, column)
		declaration.WriteString("\t" + column.field + " " + column.fieldType(file) + "\n")
	}

	declaration.WriteString("}\n\n")
	result.declare = declaration.String()

	if shared {
		result.declare = file.shareRow(name, result.declare)
		if result.declare == "\x00" {
			return shape{}, &plugin.Error{Code: "RTL-G09", Message: "query " + value.GetName() + ": @ratel.row " +
				name + " has another shape in an earlier query", Source: value.GetMeta().GetSource()}
		}
	}

	return result, nil
}

// shareRow returns the declaration to emit for a shared row: the first time
// all of it, later nothing; "\x00" when the shape differs.
func (u *unit) shareRow(name, declaration string) string {
	key := "row " + name

	previous, exists := u.names[key]
	if !exists {
		u.names[key] = declaration

		return declaration
	}

	if previous != declaration {
		return "\x00"
	}

	return ""
}

// userShape scans into the user's struct from @go.row; its fields are the
// result columns' names, so the Go compiler checks names and types.
func (s *state) userShape(file *unit, value *ir.Query, reference string) (shape, error) {
	mapped := override(reference)
	result := shape{typeName: file.use(mapped)}

	for index := range value.GetColumns() {
		column, err := s.column(file, value, index)
		if err != nil {
			return shape{}, err
		}

		column.target = "&model." + column.field
		result.columns = append(result.columns, column)
	}

	return result, nil
}

// embeddedShape is a Row embedding the table model plus computed columns.
func (s *state) embeddedShape(file *unit, value *ir.Query, group tableGroup, rest []int) (shape, error) {
	model := s.modelType(file, group.table)
	embedded := model[strings.LastIndex(model, ".")+1:]
	name := value.GetName() + "Row"

	targets := s.groupTargets(file, value, group, "model."+embedded)
	result := shape{typeName: name}

	var declaration strings.Builder

	declaration.WriteString("// " + name + " is a row of " + value.GetName() + ".\ntype " + name + " struct {\n\t" +
		model + "\n")

	extra := make(map[int]resultColumn, len(rest))

	for _, index := range rest {
		column, err := s.column(file, value, index)
		if err != nil {
			return shape{}, err
		}

		column.target = "&model." + column.field

		if column.nullable {
			column.null = s.nullTarget(file, column.typ, column.goType)
		}

		extra[index] = column
		declaration.WriteString("\t" + column.field + " " + column.fieldType(file) + "\n")
	}

	declaration.WriteString("}\n\n")
	result.declare = declaration.String()

	for index := range value.GetColumns() {
		if computed, ok := extra[index]; ok {
			result.columns = append(result.columns, computed)
		} else {
			result.columns = append(result.columns, targets[index])
		}
	}

	return result, nil
}

// joinedShape is a Row with one model per table instance; a table on the
// nullable side of an outer join is a pointer, nil when its key is NULL.
func (s *state) joinedShape(file *unit, value *ir.Query, groups []tableGroup) (shape, error) {
	name := value.GetName() + "Row"
	result := shape{typeName: name}
	targets := make(map[int]resultColumn)

	var declaration, before, after strings.Builder

	declaration.WriteString("// " + name + " is a row of " + value.GetName() + ".\ntype " + name + " struct {\n")

	seen := make(map[string]bool)

	// A field is named after its model; a table joined twice is named after
	// each alias instead.
	tables := make(map[*ir.Table]int, len(groups))
	for _, group := range groups {
		tables[group.table]++
	}

	for _, group := range groups {
		model := s.modelType(file, group.table)

		field := singular(objectOf(group.table.GetName()))
		if tables[group.table] > 1 {
			field = pascal(group.alias)
		}

		if field == "" || seen[field] {
			return shape{}, &plugin.Error{Code: "RTL-G08", Message: "query " + value.GetName() +
				": two joined tables map to field " + field + "; alias them", Source: value.GetMeta().GetSource()}
		}

		seen[field] = true

		if !groupNullable(group, value) {
			declaration.WriteString("\t" + field + " " + model + "\n")

			for index, target := range s.groupTargets(file, value, group, "model."+field) {
				targets[index] = target
			}

			continue
		}

		declaration.WriteString("\t" + field + " *" + model + "\n")
		s.nullableGroup(file, value, group, nullable{field: field, model: model}, targets, &before, &after)
	}

	declaration.WriteString("}\n\n")
	result.declare, result.before, result.after = declaration.String(), before.String(), after.String()

	for index := range value.GetColumns() {
		result.columns = append(result.columns, targets[index])
	}

	return result, nil
}

// nullable names an outer-joined table's field and model type in a Row.
type nullable struct{ field, model string }

// nullableGroup scans an outer-joined table into temporaries and builds its
// model only when its first key column is not NULL.
func (s *state) nullableGroup(file *unit, value *ir.Query, group tableGroup, target nullable,
	targets map[int]resultColumn, before, after *strings.Builder,
) {
	key := primaryKey(group.table)
	assignments := make([]string, 0, len(group.indexes))
	check := "false"

	for _, index := range group.indexes {
		name := value.GetColumns()[index].GetSourceColumn().GetName().GetParts()[2]
		temporary := camel(target.field) + pascal(name)
		targets[index] = resultColumn{name: value.GetColumns()[index].GetName(), target: "&" + temporary}

		declared := group.table.GetColumns()[slices.IndexFunc(group.table.GetColumns(),
			func(c *ir.Column) bool { return c.GetName() == name })]
		mapped, _ := s.columnType(group.table.GetName(), declared, "")
		s.use(file, declared.GetType())

		// Every column of an outer-joined table can be NULL, so each scans
		// through a temporary; the model is built only when its key is set.
		temp := s.nullTarget(file, declared.GetType(), mapped)
		before.WriteString("\tvar " + temporary + " " + temp.temp + "\n")

		assigned := temp.valueOf(temporary)
		if declared.GetNullable() {
			assigned = temp.null(temporary)
		}

		assignments = append(assignments, pascal(name)+": "+assigned)

		if len(key) > 0 && name == key[0] {
			check = temp.valid(temporary)
		}
	}

	after.WriteString("\tif " + check + " {\n\t\tmodel." + target.field + " = &" + target.model + "{" +
		strings.Join(assignments, ", ") + "}\n\t}\n\n")
}
