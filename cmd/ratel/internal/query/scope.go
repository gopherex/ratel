package query

import (
	"strconv"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
)

type scope struct {
	parent      *scope
	columns     []*ir.QueryColumn
	qualified   []*ir.QueryColumn
	keepUnknown bool
	windows     []*pg.WindowDef
	windowDefs  map[string]*pg.WindowDef
	ctes        map[string][]*ir.QueryColumn
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, ctes: make(map[string][]*ir.QueryColumn)}
}

func stringsOf(nodes []*pg.Node) []string {
	result := make([]string, len(nodes))
	for index, node := range nodes {
		result[index] = node.GetString_().GetSval()
	}

	return result
}

func copyColumns(columns []*ir.QueryColumn) []*ir.QueryColumn {
	result := make([]*ir.QueryColumn, len(columns))
	for index, column := range columns {
		result[index] = proto.CloneOf(column)
	}

	return result
}

func rename(columns []*ir.QueryColumn, alias *pg.Alias) []*ir.QueryColumn {
	result := copyColumns(columns)
	for index, column := range result {
		if alias.GetAliasname() != "" {
			column.TableAlias = alias.GetAliasname()
		}

		if index < len(alias.GetColnames()) {
			column.Name = alias.GetColnames()[index].GetString_().GetSval()
		}
	}

	return result
}

func visibleColumns(env *scope, qualifier string) []*ir.QueryColumn {
	if qualifier != "" {
		for _, col := range env.qualified {
			if col.GetTableAlias() == qualifier {
				return env.qualified
			}
		}
	}

	return env.columns
}

func (s *inference) column(parts []string, env *scope) *ir.QueryColumn {
	name := parts[len(parts)-1]

	qualifier := ""
	if len(parts) > 1 {
		qualifier = parts[len(parts)-2]
	}

	for current := env; current != nil; current = current.parent {
		var result *ir.QueryColumn

		columns := visibleColumns(current, qualifier)

		for _, col := range columns {
			if col.GetName() != name || (qualifier != "" && qualifier != col.GetTableAlias()) {
				continue
			}

			if result != nil {
				s.fail("ambiguous column " + strings.Join(parts, "."))

				return &ir.QueryColumn{}
			}

			result = col
		}

		if result != nil {
			return proto.CloneOf(result)
		}
	}

	s.fail("column " + strings.Join(parts, "."))

	return &ir.QueryColumn{}
}

func (s *inference) relation(name *pg.RangeVar, env *scope) []*ir.QueryColumn {
	if name.GetSchemaname() == "" {
		for current := env; current != nil; current = current.parent {
			if columns, exists := current.ctes[name.GetRelname()]; exists {
				return rename(columns, &pg.Alias{Aliasname: name.GetRelname()})
			}
		}
	}

	schemas := s.catalog.GetSearchPath()
	if name.GetSchemaname() != "" {
		schemas = []string{name.GetSchemaname()}
	}

	if len(schemas) == 0 {
		schemas = []string{"public"}
	}

	for _, namespace := range schemas {
		for _, schema := range s.catalog.GetSchemas() {
			if schema.GetName() != namespace {
				continue
			}

			if columns := s.schemaRelation(schema, name.GetRelname()); columns != nil {
				if name.GetSchemaname() == "" {
					name.Schemaname = namespace
					s.qualified = true
				}

				return columns
			}
		}
	}

	s.fail("relation " + name.GetSchemaname() + "." + name.GetRelname())

	return nil
}

func (s *inference) schemaRelation(schema *ir.Schema, name string) []*ir.QueryColumn {
	for _, table := range schema.GetTables() {
		parts := table.GetName().GetParts()
		if parts[len(parts)-1] == name {
			return tableColumns(table, name)
		}
	}

	for _, view := range schema.GetViews() {
		parts := view.GetName().GetParts()
		if parts[len(parts)-1] == name {
			return s.view(view.GetQuery(), view.GetColumns(), name)
		}
	}

	for _, view := range schema.GetMaterializedViews() {
		parts := view.GetName().GetParts()
		if parts[len(parts)-1] == name {
			return s.view(view.GetQuery(), view.GetColumns(), name)
		}
	}

	return nil
}

func tableColumns(table *ir.Table, alias string) []*ir.QueryColumn {
	result := make([]*ir.QueryColumn, 0, len(table.GetColumns()))

	for _, column := range table.GetColumns() {
		parts := append([]string{}, table.GetName().GetParts()...)
		result = append(result, &ir.QueryColumn{
			Name: column.GetName(), Type: proto.CloneOf(column.GetType()), Nullable: column.GetNullable(), TableAlias: alias,
			SourceColumn: &ir.ObjectRef{
				Kind: ir.ObjectKind_OBJECT_KIND_COLUMN,
				Name: &ir.QualifiedName{Parts: append(parts, column.GetName())},
			},
		})
	}

	return result
}

func (s *inference) view(query *ir.SelectStmt, names []string, alias string) []*ir.QueryColumn {
	tree, err := s.parser.Parse(query.GetRawSql())
	if err != nil || len(tree.GetStmts()) != 1 {
		s.fail("view " + alias)

		return nil
	}

	columns := s.statement(tree.GetStmts()[0].GetStmt(), newScope(nil))
	for index, col := range columns {
		col.TableAlias = alias
		if index < len(names) {
			col.Name = names[index]
		}
	}

	return columns
}

func (s *inference) from(node *pg.Node, env *scope) []*ir.QueryColumn {
	var result []*ir.QueryColumn

	switch {
	case node.GetRangeVar() != nil:
		table := node.GetRangeVar()
		result = rename(s.relation(table, env), table.GetAlias())
	case node.GetRangeSubselect() != nil:
		subquery := node.GetRangeSubselect()
		parent := namespaceScope(env)

		if subquery.GetLateral() {
			parent = env
		}

		result = rename(s.statement(subquery.GetSubquery(), newScope(parent)), subquery.GetAlias())
	case node.GetJoinExpr() != nil:
		return s.join(node.GetJoinExpr(), env)
	case node.GetRangeFunction() != nil:
		result = s.rangeFunction(node.GetRangeFunction(), env)
	default:
		s.fail("FROM expression " + node.String())
	}

	env.qualified = append(env.qualified, copyColumns(result)...)

	return result
}

func (s *inference) join(join *pg.JoinExpr, env *scope) []*ir.QueryColumn {
	start := len(env.qualified)
	left := s.from(join.GetLarg(), env)
	rightScope := newScope(env)
	rightScope.columns = left
	right := s.from(join.GetRarg(), rightScope)
	on := newScope(env)

	on.columns = append(copyColumns(left), right...)
	on.qualified = append(copyColumns(env.qualified[start:]), rightScope.qualified...)
	s.expr(join.GetQuals(), on, scalar("bool"))
	using := stringsOf(join.GetUsingClause())

	if join.GetIsNatural() {
		for _, lcol := range left {
			for _, rcol := range right {
				if lcol.GetName() == rcol.GetName() {
					using = append(using, lcol.GetName())
				}
			}
		}
	}

	result := s.joinColumns(left, right, using, join.GetJointype())

	if join.GetJointype() == pg.JoinType_JOIN_LEFT || join.GetJointype() == pg.JoinType_JOIN_FULL {
		for _, col := range append(right, rightScope.qualified...) {
			col.Nullable = true
		}
	}

	if join.GetJointype() == pg.JoinType_JOIN_RIGHT || join.GetJointype() == pg.JoinType_JOIN_FULL {
		for _, col := range append(left, env.qualified[start:]...) {
			col.Nullable = true
		}
	}

	env.qualified = append(env.qualified, rightScope.qualified...)
	result = rename(result, join.GetAlias())

	if join.GetAlias().GetAliasname() != "" {
		env.qualified = append(env.qualified[:start], copyColumns(result)...)
	}

	return result
}

func (s *inference) joinColumns(left, right []*ir.QueryColumn, using []string, kind pg.JoinType) []*ir.QueryColumn {
	result := make([]*ir.QueryColumn, 0, len(left)+len(right))
	merged := make(map[string]bool, len(using))

	for _, name := range using {
		var lcol, rcol *ir.QueryColumn

		for _, col := range left {
			if col.GetName() == name {
				lcol = col
			}
		}

		for _, col := range right {
			if col.GetName() == name {
				rcol = col
			}
		}

		if lcol == nil || rcol == nil {
			s.fail("JOIN USING " + name)

			continue
		}

		col := proto.CloneOf(lcol)
		col.Type = s.common([]*ir.QueryColumn{lcol, rcol})

		if kind == pg.JoinType_JOIN_RIGHT {
			col = proto.CloneOf(rcol)
		}

		if kind == pg.JoinType_JOIN_FULL {
			col.SourceColumn = nil
			col.TableAlias = ""
			col.Nullable = lcol.GetNullable() || rcol.GetNullable()
		}

		result = append(result, col)
		merged[name] = true
	}

	for _, col := range append(left, right...) {
		if !merged[col.GetName()] {
			result = append(result, col)
		}
	}

	return result
}

func (s *inference) rangeFunction(function *pg.RangeFunction, env *scope) []*ir.QueryColumn {
	var result []*ir.QueryColumn

	for _, entry := range function.GetFunctions() {
		items := entry.GetList().GetItems()
		if len(items) == 0 {
			continue
		}

		call := items[0].GetFuncCall()
		args := s.expressions(call.GetArgs(), env, nil)
		chosen := s.function(stringsOf(call.GetFuncname()), args, call.GetFuncVariadic())
		s.constrain(call.GetArgs(), nil, chosen.inputs, env)
		name := stringsOf(call.GetFuncname())
		last := ""

		if len(name) > 0 {
			last = name[len(name)-1]
		}

		defaultName := last
		if len(function.GetFunctions()) == 1 && function.GetAlias().GetAliasname() != "" {
			defaultName = function.GetAlias().GetAliasname()
		}

		outputs := s.functionColumns(chosen, defaultName)
		if len(items) > 1 && len(items[1].GetList().GetItems()) > 0 {
			outputs = s.recordColumns(items[1].GetList().GetItems())
		}

		for _, column := range outputs {
			column.TableAlias = last
		}

		result = append(result, outputs...)
	}

	if function.GetOrdinality() {
		alias := ""
		if len(result) > 0 {
			alias = result[0].GetTableAlias()
		}

		result = append(result, &ir.QueryColumn{Name: "ordinality", Type: scalar("int8"), TableAlias: alias})
	}

	if len(function.GetColdeflist()) > 0 {
		result = s.recordColumns(function.GetColdeflist())
	}

	return rename(result, function.GetAlias())
}

func (s *inference) recordColumns(nodes []*pg.Node) []*ir.QueryColumn {
	result := make([]*ir.QueryColumn, 0, len(nodes))

	for _, node := range nodes {
		column := node.GetColumnDef()
		result = append(result, &ir.QueryColumn{
			Name: column.GetColname(), Type: s.resolveType(mapper.MapType(column.GetTypeName())), Nullable: true,
		})
	}

	return result
}

func (s *inference) functionColumns(chosen *candidate, name string) []*ir.QueryColumn {
	if columns := functionOutputs(chosen.function); len(columns) > 0 {
		return columns
	}

	typ := s.signatures.baseType(chosen.result)
	if typ.GetKind() != ir.TypeKind_TYPE_KIND_COMPOSITE {
		return []*ir.QueryColumn{{Name: name, Type: chosen.result, Nullable: true}}
	}

	columns := s.compositeColumns(typ)
	if len(columns) == 0 {
		s.fail("composite table function result " + typeKey(typ))
	}

	for _, column := range columns {
		column.SourceColumn = nil
		column.Nullable = true
	}

	return columns
}

func (s *inference) compositeColumns(typ *ir.TypeRef) []*ir.QueryColumn {
	key := typeKey(typ)

	for _, schema := range s.catalog.GetSchemas() {
		for _, table := range schema.GetTables() {
			if nameKey(table.GetName().GetParts()) == key {
				return tableColumns(table, "")
			}
		}

		for _, composite := range schema.GetComposites() {
			if nameKey(composite.GetName().GetParts()) != key {
				continue
			}

			columns := make([]*ir.QueryColumn, 0, len(composite.GetFields()))
			for _, field := range composite.GetFields() {
				columns = append(columns, &ir.QueryColumn{Name: field.GetName(), Type: proto.CloneOf(field.GetType())})
			}

			return columns
		}

		for _, view := range schema.GetViews() {
			if nameKey(view.GetName().GetParts()) == key {
				return s.view(view.GetQuery(), view.GetColumns(), "")
			}
		}

		for _, view := range schema.GetMaterializedViews() {
			if nameKey(view.GetName().GetParts()) == key {
				return s.view(view.GetQuery(), view.GetColumns(), "")
			}
		}
	}

	return nil
}

func functionOutputs(function *ir.Function) []*ir.QueryColumn {
	var result []*ir.QueryColumn

	if function.GetReturns().GetTable() == nil {
		for _, arg := range function.GetArguments() {
			if arg.GetMode() == ir.ArgMode_ARG_MODE_OUT || arg.GetMode() == ir.ArgMode_ARG_MODE_INOUT {
				result = append(result, &ir.QueryColumn{Name: arg.GetName(), Type: arg.GetType(), Nullable: true})
			}
		}
	}

	for _, col := range function.GetReturns().GetTable().GetColumns() {
		result = append(result, &ir.QueryColumn{Name: col.GetName(), Type: col.GetType(), Nullable: true})
	}

	return result
}

func (s *inference) targets(nodes []*pg.Node, env *scope) []*ir.QueryColumn {
	var result []*ir.QueryColumn

	for _, node := range nodes {
		target := node.GetResTarget()

		fields := target.GetVal().GetColumnRef().GetFields()
		if len(fields) > 0 && fields[len(fields)-1].GetAStar() != nil {
			qualifier := ""
			if len(fields) > 1 {
				qualifier = fields[len(fields)-2].GetString_().GetSval()
			}

			before := len(result)

			columns := visibleColumns(env, qualifier)
			for _, col := range columns {
				if qualifier == "" || col.GetTableAlias() == qualifier {
					result = append(result, proto.CloneOf(col))
				}
			}

			if len(result) == before {
				s.fail("star projection " + qualifier)
			}

			continue
		}

		col := s.expr(target.GetVal(), env, nil)
		if target.GetName() != "" {
			col.Name = target.GetName()
		}

		if col.GetName() == "" {
			col.Name = "?column?"
		}

		result = append(result, col)
	}

	return result
}

func (s *inference) values(rows []*pg.Node, env *scope, expected []*ir.QueryColumn) []*ir.QueryColumn {
	var result []*ir.QueryColumn

	for _, row := range rows {
		values := row.GetList().GetItems()
		if result == nil {
			result = make([]*ir.QueryColumn, len(values))
		}

		if len(result) != len(values) {
			s.fail("VALUES row width")

			continue
		}

		s.expressions(values, env, nil)

		for index, node := range values {
			var context *ir.QueryColumn
			if index < len(expected) {
				context = expected[index]
			}

			value := s.context(node, env, context)
			if result[index] == nil {
				result[index] = value
				value.Name = "column" + strconv.Itoa(index+1)

				continue
			}

			current := result[index]
			current.Type = s.common([]*ir.QueryColumn{current, value})
			current.Nullable = current.GetNullable() || value.GetNullable()
		}
	}

	for _, column := range result {
		if unknown(column.GetType()) {
			column.Type = scalar("text")
		}
	}

	s.valueContext(rows, env, result)

	return result
}

func (s *inference) valueContext(rows []*pg.Node, env *scope, expected []*ir.QueryColumn) {
	for _, row := range rows {
		for index, node := range row.GetList().GetItems() {
			if index < len(expected) {
				s.context(node, env, expected[index])
			}
		}
	}
}

func namespaceScope(env *scope) *scope {
	return &scope{parent: env.parent, ctes: env.ctes}
}
