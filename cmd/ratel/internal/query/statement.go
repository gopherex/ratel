package query

import (
	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"
)

func (s *inference) statement(node *pg.Node, env *scope) []*ir.QueryColumn {
	if s.err != nil {
		return nil
	}

	switch {
	case node.GetSelectStmt() != nil:
		return s.selectStmt(node.GetSelectStmt(), env, nil)
	case node.GetInsertStmt() != nil:
		return s.insert(node.GetInsertStmt(), env)
	case node.GetUpdateStmt() != nil:
		return s.update(node.GetUpdateStmt(), env)
	case node.GetDeleteStmt() != nil:
		return s.deleteStmt(node.GetDeleteStmt(), env)
	case node.GetMergeStmt() != nil:
		return s.merge(node.GetMergeStmt(), env)
	default:
		s.fail("statement " + node.String())

		return nil
	}
}

func (s *inference) with(with *pg.WithClause, env *scope) {
	for _, node := range with.GetCtes() {
		cte := node.GetCommonTableExpr()

		query := cte.GetCtequery().GetSelectStmt()
		if with.GetRecursive() && query.GetLarg() != nil {
			env.ctes[cte.GetCtename()] = s.recursive(cte, env)

			continue
		}

		columns := s.statement(cte.GetCtequery(), newScope(env))
		env.ctes[cte.GetCtename()] = rename(columns, &pg.Alias{Colnames: cte.GetAliascolnames()})
	}
}

func (s *inference) recursive(cte *pg.CommonTableExpr, env *scope) []*ir.QueryColumn {
	query := cte.GetCtequery().GetSelectStmt()
	anchor := s.selectStmt(query.GetLarg(), newScope(env), nil)
	alias := &pg.Alias{Colnames: cte.GetAliascolnames()}
	previous := rename(anchor, alias)
	// Each iteration can only add nullable columns; at most one addition per column is needed.
	for range len(anchor) + 1 {
		env.ctes[cte.GetCtename()] = previous
		next := rename(s.statement(cte.GetCtequery(), newScope(env)), alias)

		if s.err != nil {
			return nil
		}

		changed := false

		for index, col := range next {
			if typeKey(col.GetType()) != typeKey(anchor[index].GetType()) {
				s.fail("recursive CTE column " + col.GetName())

				return nil
			}

			col.Type = anchor[index].GetType()
			col.Nullable = col.GetNullable() || previous[index].GetNullable()
			changed = changed || col.GetNullable() != previous[index].GetNullable()
		}

		if !changed {
			return next
		}

		previous = next
	}

	return previous
}

func (s *inference) selectStmt(stmt *pg.SelectStmt, env *scope, expected []*ir.QueryColumn) []*ir.QueryColumn {
	s.with(stmt.GetWithClause(), env)

	var result []*ir.QueryColumn

	switch {
	case stmt.GetLarg() != nil:
		result = s.setOperation(stmt, env)
	case len(stmt.GetValuesLists()) > 0:
		result = s.values(stmt.GetValuesLists(), env, expected)
	default:
		result = s.simpleSelect(stmt, env)
	}

	result = s.selectTail(stmt, env, result, expected)
	if !env.keepUnknown {
		for _, column := range result {
			if column.GetType().GetPgName() == unknownType {
				column.Type = scalar("text")
			}
		}
	}

	return result
}

func (s *inference) simpleSelect(stmt *pg.SelectStmt, env *scope) []*ir.QueryColumn {
	env.windowDefs = make(map[string]*pg.WindowDef, len(stmt.GetWindowClause()))

	for _, node := range stmt.GetWindowClause() {
		window := node.GetWindowDef()
		env.windowDefs[window.GetName()] = window
	}

	for _, from := range stmt.GetFromClause() {
		env.columns = append(env.columns, s.from(from, env)...)
	}
	// PostgreSQL resolves target expressions before WHERE; overloads can bind a parameter's type here.
	s.targets(stmt.GetTargetList(), env)
	s.expr(stmt.GetWhereClause(), env, scalar("bool"))
	s.expr(stmt.GetHavingClause(), env, scalar("bool"))

	return s.targets(stmt.GetTargetList(), env)
}

func (s *inference) selectTail(
	stmt *pg.SelectStmt, env *scope, result, expected []*ir.QueryColumn,
) []*ir.QueryColumn {
	order := newScope(env)
	order.columns = result

	for _, node := range stmt.GetSortClause() {
		s.ordering(node.GetSortBy().GetNode(), order)
	}

	simple := stmt.GetLarg() == nil && len(stmt.GetValuesLists()) == 0
	if simple {
		s.selectClauses(stmt, env)
		markGroupingNullable(stmt.GetGroupClause(), env)
	}

	s.expr(stmt.GetLimitCount(), env, scalar("int8"))
	s.expr(stmt.GetLimitOffset(), env, scalar("int8"))

	for _, node := range stmt.GetWindowClause() {
		s.window(node.GetWindowDef(), env)
	}

	for _, window := range append(env.windows, order.windows...) {
		s.window(window, env)
	}

	if simple {
		s.targetContext(stmt.GetTargetList(), env, expected)
		result = s.targets(stmt.GetTargetList(), env)
	}

	return result
}

func (s *inference) selectClauses(selectStmt *pg.SelectStmt, env *scope) {
	if len(selectStmt.GetGroupClause())+len(selectStmt.GetDistinctClause()) == 0 {
		return
	}

	targets := s.targets(selectStmt.GetTargetList(), env)
	aliases := newScope(namespaceScope(env))
	aliases.columns = targets
	group := newScope(aliases)
	group.columns = env.columns
	group.qualified = env.qualified
	s.grouping(selectStmt.GetGroupClause(), group)

	distinct := newScope(env)
	distinct.columns = targets

	for _, node := range selectStmt.GetDistinctClause() {
		s.ordering(node, distinct)
	}

	env.windows = append(env.windows, group.windows...)
	env.windows = append(env.windows, distinct.windows...)
}

func (s *inference) targetContext(targets []*pg.Node, env *scope, expected []*ir.QueryColumn) {
	if env.keepUnknown && len(expected) == 0 {
		return
	}

	index := 0

	for _, target := range targets {
		fields := target.GetResTarget().GetVal().GetColumnRef().GetFields()
		if len(fields) > 0 && fields[len(fields)-1].GetAStar() != nil {
			index += len(s.targets([]*pg.Node{target}, env))

			continue
		}

		value := target.GetResTarget().GetVal()
		if index < len(expected) {
			s.context(value, env, expected[index])
		} else if value.GetParamRef() != nil && s.refTypes[value.GetParamRef()] == nil {
			s.parameter(value.GetParamRef(), scalar("text"))
		}

		index++
	}
}

func (s *inference) returning(nodes []*pg.Node, env *scope) []*ir.QueryColumn {
	s.targets(nodes, env)
	s.targetContext(nodes, env, nil)

	return s.targets(nodes, env)
}

func (s *inference) setOperation(selectStmt *pg.SelectStmt, env *scope) []*ir.QueryColumn {
	leftScope, rightScope := newScope(env), newScope(env)
	leftScope.keepUnknown = true
	rightScope.keepUnknown = true
	left := s.selectStmt(selectStmt.GetLarg(), leftScope, nil)
	right := s.selectStmt(selectStmt.GetRarg(), rightScope, nil)

	if len(left) != len(right) {
		s.fail("set operation column count")

		return nil
	}

	for index, col := range left {
		col.Type = s.common([]*ir.QueryColumn{col, right[index]})
		if selectStmt.GetOp() == pg.SetOperation_SETOP_UNION {
			col.Nullable = col.GetNullable() || right[index].GetNullable()
		}

		col.SourceColumn = nil
		col.TableAlias = ""
	}

	s.targetContext(selectStmt.GetLarg().GetTargetList(), leftScope, left)
	s.targetContext(selectStmt.GetRarg().GetTargetList(), rightScope, left)
	s.valueContext(selectStmt.GetLarg().GetValuesLists(), leftScope, left)
	s.valueContext(selectStmt.GetRarg().GetValuesLists(), rightScope, left)

	return left
}

func (s *inference) insert(insert *pg.InsertStmt, env *scope) []*ir.QueryColumn {
	s.with(insert.GetWithClause(), env)
	table := insert.GetRelation()
	columns := s.relation(table, env)
	env.columns = rename(columns, table.GetAlias())
	expected := s.writeColumns(columns, insert.GetCols(), env)
	source := insert.GetSelectStmt().GetSelectStmt()

	if source != nil {
		s.selectStmt(source, newScope(namespaceScope(env)), expected)
	}

	conflict := insert.GetOnConflictClause()
	excluded := rename(columns, &pg.Alias{Aliasname: "excluded"})
	conflictScope := newScope(env)
	conflictScope.columns = append(copyColumns(env.columns), excluded...)
	s.assignments(conflict.GetTargetList(), columns, conflictScope)
	s.expr(conflict.GetWhereClause(), conflictScope, scalar("bool"))
	s.expr(conflict.GetInfer().GetWhereClause(), conflictScope, scalar("bool"))

	return s.returning(insert.GetReturningList(), env)
}

func (s *inference) writeColumns(columns []*ir.QueryColumn, targets []*pg.Node, env *scope) []*ir.QueryColumn {
	if len(targets) == 0 {
		return columns
	}

	result := make([]*ir.QueryColumn, 0, len(targets))

	for _, target := range targets {
		name := target.GetResTarget().GetName()
		found := false

		for _, col := range columns {
			if col.GetName() == name {
				result = append(result, s.writeTarget(col, target.GetResTarget(), env))
				found = true

				break
			}
		}

		if !found {
			s.fail("write column " + name)
		}
	}

	return result
}

func (s *inference) writeTarget(column *ir.QueryColumn, target *pg.ResTarget, env *scope) *ir.QueryColumn {
	if len(target.GetIndirection()) == 0 {
		return column
	}

	result := proto.CloneOf(column)
	slice := false
	indexType := &ir.QueryColumn{Type: scalar("int4")}

	for _, node := range target.GetIndirection() {
		index := node.GetAIndices()
		if index == nil {
			s.fail("assignment field " + node.String())

			continue
		}

		s.context(index.GetLidx(), env, indexType)
		s.context(index.GetUidx(), env, indexType)
		slice = slice || index.GetIsSlice()
	}

	if result.GetType().GetElement() == nil {
		s.fail("array assignment " + column.GetName())

		return result
	}

	if !slice {
		result.Type = result.GetType().GetElement()
	}

	result.Nullable = true

	return result
}

func (s *inference) assignments(targets []*pg.Node, columns []*ir.QueryColumn, env *scope) {
	for _, target := range targets {
		value := target.GetResTarget().GetVal()
		if multi := value.GetMultiAssignRef(); multi != nil {
			if multi.GetColno() == 1 {
				s.tupleSource(multi.GetSource(), env, nil)
			}
		} else {
			s.expr(value, env, nil)
		}
	}

	expected := s.writeColumns(columns, targets, env)

	for index, target := range targets {
		value := target.GetResTarget().GetVal()
		if multi := value.GetMultiAssignRef(); multi != nil {
			if multi.GetColno() != 1 {
				continue
			}

			end := index + int(multi.GetNcolumns())
			if end > len(expected) {
				s.fail("assignment row width")

				continue
			}

			s.tupleSource(multi.GetSource(), env, expected[index:end])
		} else if index < len(expected) {
			s.context(value, env, expected[index])
		}
	}
}

func (s *inference) tupleSource(source *pg.Node, env *scope, expected []*ir.QueryColumn) {
	if source.GetRowExpr() != nil {
		nodes := source.GetRowExpr().GetArgs()
		s.expressions(nodes, env, nil)

		if len(expected) > 0 && len(nodes) != len(expected) {
			s.fail("assignment row width")

			return
		}

		for index, node := range nodes {
			if index < len(expected) {
				s.context(node, env, expected[index])
			}
		}

		return
	}

	if source.GetSubLink() != nil {
		columns := s.statement(source.GetSubLink().GetSubselect(), newScope(env))
		if len(expected) > 0 && len(columns) != len(expected) {
			s.fail("assignment subquery width")
		}

		return
	}

	s.fail("assignment source " + source.String())
}

func (s *inference) update(update *pg.UpdateStmt, env *scope) []*ir.QueryColumn {
	s.with(update.GetWithClause(), env)
	columns := s.relation(update.GetRelation(), env)
	env.columns = rename(columns, update.GetRelation().GetAlias())

	for _, from := range update.GetFromClause() {
		env.columns = append(env.columns, s.from(from, env)...)
	}

	s.assignments(update.GetTargetList(), columns, env)
	s.expr(update.GetWhereClause(), env, scalar("bool"))

	return s.returning(update.GetReturningList(), env)
}

func (s *inference) deleteStmt(statement *pg.DeleteStmt, env *scope) []*ir.QueryColumn {
	s.with(statement.GetWithClause(), env)

	env.columns = rename(s.relation(statement.GetRelation(), env), statement.GetRelation().GetAlias())
	for _, from := range statement.GetUsingClause() {
		env.columns = append(env.columns, s.from(from, env)...)
	}

	s.expr(statement.GetWhereClause(), env, scalar("bool"))

	return s.returning(statement.GetReturningList(), env)
}

func (s *inference) merge(merge *pg.MergeStmt, env *scope) []*ir.QueryColumn {
	s.with(merge.GetWithClause(), env)
	columns := s.relation(merge.GetRelation(), env)
	env.columns = rename(columns, merge.GetRelation().GetAlias())
	env.columns = append(env.columns, s.from(merge.GetSourceRelation(), env)...)
	s.expr(merge.GetJoinCondition(), env, scalar("bool"))

	sourceNullable := false

	for _, node := range merge.GetMergeWhenClauses() {
		clause := node.GetMergeWhenClause()
		sourceNullable = sourceNullable || clause.GetMatchKind() == pg.MergeMatchKind_MERGE_WHEN_NOT_MATCHED_BY_SOURCE
		s.expr(clause.GetCondition(), env, scalar("bool"))
		s.assignments(clause.GetTargetList(), columns, env)

		expected := s.writeColumns(columns, clause.GetTargetList(), env)
		for index, value := range clause.GetValues() {
			if index < len(expected) {
				s.context(value, env, expected[index])
			}
		}
	}

	if sourceNullable {
		for _, col := range append(env.columns[len(columns):], env.qualified...) {
			col.Nullable = true
		}
	}

	return s.returning(merge.GetReturningList(), env)
}
