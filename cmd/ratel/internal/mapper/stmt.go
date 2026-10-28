package mapper

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"

	"github.com/gopherex/ratel/ir"
)

// MapStatement maps DML structurally and preserves other statements as raw SQL.
// Schema declarations use the dedicated DDL mapping methods instead.
func (m Mapper) MapStatement(node *pg.Node) *ir.Statement {
	result := m.statement(node)
	if result.GetSelect() != nil {
		result.GetSelect().RawSql = m.SourceSQL()
	}

	if result.GetRaw() != nil {
		result.GetRaw().Sql = m.SourceSQL()
	}

	m.finish(result)

	return result
}

func (m Mapper) statement(node *pg.Node) *ir.Statement {
	result := &ir.Statement{}

	switch {
	case node.GetSelectStmt() != nil:
		result.Statement = &ir.Statement_Select{Select: m.selectStmt(node.GetSelectStmt())}
	case node.GetInsertStmt() != nil:
		result.Statement = &ir.Statement_Insert{Insert: m.insert(node.GetInsertStmt())}
	case node.GetUpdateStmt() != nil:
		result.Statement = &ir.Statement_Update{Update: m.update(node.GetUpdateStmt())}
	case node.GetDeleteStmt() != nil:
		result.Statement = &ir.Statement_Delete{Delete: m.deleteStmt(node.GetDeleteStmt())}
	case node.GetMergeStmt() != nil:
		result.Statement = &ir.Statement_Merge{Merge: m.merge(node.GetMergeStmt())}
	default:
		result.Statement = &ir.Statement_Raw{Raw: &ir.RawStatement{Kind: classify(node), Sql: m.statementSQL(node)}}
	}

	return result
}

func (m Mapper) statementSQL(node *pg.Node) string {
	sql, err := pgquery.Deparse(&pg.ParseResult{Stmts: []*pg.RawStmt{{Stmt: node}}})
	if err != nil {
		return m.sql[m.start:m.end]
	}

	return sql
}

func (m Mapper) selectStmt(value *pg.SelectStmt) *ir.SelectStmt {
	if value == nil {
		return nil
	}

	result := &ir.SelectStmt{
		OrderBy: m.orders(value.GetSortClause()),
		Limit:   m.expr(value.GetLimitCount()), Offset: m.expr(value.GetLimitOffset()),
		RawSql: m.statementSQL(&pg.Node{Node: &pg.Node_SelectStmt{SelectStmt: value}}),
	}
	result.With, result.WithRecursive = m.with(value.GetWithClause())

	switch value.GetOp() {
	case pg.SetOperation_SETOP_UNION, pg.SetOperation_SETOP_INTERSECT, pg.SetOperation_SETOP_EXCEPT:
		kind := ir.SetOpKind_SET_OP_KIND_UNION
		if value.GetOp() == pg.SetOperation_SETOP_INTERSECT {
			kind = ir.SetOpKind_SET_OP_KIND_INTERSECT
		}

		if value.GetOp() == pg.SetOperation_SETOP_EXCEPT {
			kind = ir.SetOpKind_SET_OP_KIND_EXCEPT
		}

		result.Body = &ir.SelectStmt_SetOperation{SetOperation: &ir.SetOperation{
			Kind: kind, All: value.GetAll(),
			Left: m.selectStmt(value.GetLarg()), Right: m.selectStmt(value.GetRarg()),
		}}
	default:
		if len(value.GetValuesLists()) > 0 {
			result.Body = &ir.SelectStmt_Values{Values: m.values(value.GetValuesLists())}
		} else {
			result.Body = &ir.SelectStmt_Select{Select: m.simpleSelect(value)}
		}
	}

	if len(value.GetLockingClause()) > 0 {
		result.Locking = locking(value.GetLockingClause()[0].GetLockingClause())
	}

	return result
}

func (m Mapper) simpleSelect(value *pg.SelectStmt) *ir.SimpleSelect {
	result := &ir.SimpleSelect{
		Distinct: len(value.GetDistinctClause()) > 0,
		Targets:  m.targets(value.GetTargetList()), From: m.fromItems(value.GetFromClause()),
		Where: m.expr(value.GetWhereClause()), GroupBy: m.expressions(value.GetGroupClause()),
		Having: m.expr(value.GetHavingClause()),
	}

	for _, node := range value.GetDistinctClause() {
		if node.GetNode() != nil {
			result.DistinctOn = append(result.DistinctOn, m.expr(node))
		}
	}
	// GroupDistinct means GROUP BY DISTINCT, not GROUP BY ALL. The IR has only ALL.
	result.GroupByAll = !value.GetGroupDistinct() && len(value.GetGroupClause()) > 0

	for _, node := range value.GetWindowClause() {
		window := node.GetWindowDef()
		spec := m.window(window)
		spec.RefName = window.GetRefname()
		result.Windows = append(result.Windows, &ir.WindowDef{Name: window.GetName(), Spec: spec})
	}

	return result
}

func (m Mapper) targets(nodes []*pg.Node) []*ir.SelectTarget {
	result := make([]*ir.SelectTarget, 0, len(nodes))

	for _, node := range nodes {
		target := node.GetResTarget()
		result = append(result, &ir.SelectTarget{Alias: target.GetName(), Expr: m.expr(target.GetVal())})
	}

	return result
}

func (m Mapper) orders(nodes []*pg.Node) []*ir.OrderByItem {
	result := make([]*ir.OrderByItem, 0, len(nodes))

	for _, node := range nodes {
		value := node.GetSortBy()
		result = append(result, &ir.OrderByItem{
			Expr: m.expr(value.GetNode()), Order: sortOrder(value.GetSortbyDir()),
			Nulls: nullsOrder(value.GetSortbyNulls()), UsingOperator: dotted(value.GetUseOp()),
		})
	}

	return result
}

func (m Mapper) window(value *pg.WindowDef) *ir.WindowSpec {
	if value == nil {
		return nil
	}

	name := value.GetRefname()
	if name == "" {
		name = value.GetName()
	}

	return &ir.WindowSpec{
		RefName: name, PartitionBy: m.expressions(value.GetPartitionClause()),
		OrderBy: m.orders(value.GetOrderClause()), Frame: m.windowFrame(value),
	}
}

func (m Mapper) windowFrame(value *pg.WindowDef) string {
	// Deparse just the frame so identifiers or literals in other clauses cannot
	// be mistaken for frame keywords. Bit 0 is FRAMEOPTION_NONDEFAULT.
	const nonDefault = 1
	if value.GetFrameOptions()&nonDefault == 0 {
		return ""
	}

	frame := &pg.WindowDef{
		FrameOptions: value.GetFrameOptions(),
		StartOffset:  value.GetStartOffset(), EndOffset: value.GetEndOffset(),
	}
	call := &pg.Node{Node: &pg.Node_FuncCall{FuncCall: &pg.FuncCall{
		Funcname: []*pg.Node{{Node: &pg.Node_String_{String_: &pg.String{Sval: "f"}}}}, Over: frame,
	}}}
	sql := m.expressionSQL(call)

	_, tail, found := strings.Cut(sql, "OVER (")
	if !found {
		return ""
	}

	return strings.TrimSuffix(tail, ")")
}

func (m Mapper) fromItems(nodes []*pg.Node) []*ir.FromItem {
	result := make([]*ir.FromItem, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, m.from(node))
	}

	return result
}

func (m Mapper) from(node *pg.Node) *ir.FromItem {
	result := &ir.FromItem{}

	switch {
	case node.GetRangeVar() != nil:
		value := node.GetRangeVar()
		result.Source = &ir.FromItem_Table{Table: &ir.TableRef{
			Name:  relationName(value),
			Alias: value.GetAlias().GetAliasname(), Only: !value.GetInh(),
		}}
	case node.GetJoinExpr() != nil:
		result.Source = &ir.FromItem_Join{Join: m.join(node.GetJoinExpr())}
	case node.GetRangeSubselect() != nil:
		value := node.GetRangeSubselect()
		result.Source = &ir.FromItem_Subquery{Subquery: &ir.SubqueryRef{
			Query: m.selectStmt(value.GetSubquery().GetSelectStmt()), Alias: value.GetAlias().GetAliasname(),
			ColumnAliases: stringsOf(value.GetAlias().GetColnames()), Lateral: value.GetLateral(),
		}}
	case node.GetRangeFunction() != nil:
		value := node.GetRangeFunction()
		calls := make([]*pg.Node, 0, len(value.GetFunctions()))

		for _, function := range value.GetFunctions() {
			items := function.GetList().GetItems()
			if len(items) > 0 {
				calls = append(calls, items[0])
			}
		}

		call := m.list(calls, false)
		if len(calls) == 1 {
			call = m.expr(calls[0])
		}

		result.Source = &ir.FromItem_Function{Function: &ir.FunctionRef{
			Call:  call,
			Alias: value.GetAlias().GetAliasname(), ColumnAliases: stringsOf(value.GetAlias().GetColnames()),
			Lateral: value.GetLateral(), WithOrdinality: value.GetOrdinality(),
		}}
	}

	return result
}

func (m Mapper) join(value *pg.JoinExpr) *ir.JoinClause {
	kind := ir.JoinType_JOIN_TYPE_UNSPECIFIED

	switch value.GetJointype() {
	case pg.JoinType_JOIN_INNER:
		kind = ir.JoinType_JOIN_TYPE_INNER
		if value.GetQuals() == nil && len(value.GetUsingClause()) == 0 && !value.GetIsNatural() {
			kind = ir.JoinType_JOIN_TYPE_CROSS
		}
	case pg.JoinType_JOIN_LEFT:
		kind = ir.JoinType_JOIN_TYPE_LEFT
	case pg.JoinType_JOIN_RIGHT:
		kind = ir.JoinType_JOIN_TYPE_RIGHT
	case pg.JoinType_JOIN_FULL:
		kind = ir.JoinType_JOIN_TYPE_FULL
	default:
	}

	return &ir.JoinClause{
		Type: kind, Left: m.from(value.GetLarg()), Right: m.from(value.GetRarg()),
		On: m.expr(value.GetQuals()), Using: stringsOf(value.GetUsingClause()), Natural: value.GetIsNatural(),
	}
}

func (m Mapper) values(nodes []*pg.Node) *ir.Values {
	result := &ir.Values{}
	for _, node := range nodes {
		result.Rows = append(result.Rows, &ir.ValuesRow{Values: m.expressions(node.GetList().GetItems())})
	}

	return result
}

func (m Mapper) with(value *pg.WithClause) ([]*ir.CommonTableExpr, bool) {
	result := make([]*ir.CommonTableExpr, 0, len(value.GetCtes()))

	for _, node := range value.GetCtes() {
		entry := node.GetCommonTableExpr()
		mapped := &ir.CommonTableExpr{Name: entry.GetCtename(), Columns: stringsOf(entry.GetAliascolnames())}

		switch entry.GetCtematerialized() {
		case pg.CTEMaterialize_CTEMaterializeAlways:
			mapped.Materialization = ir.CteMaterialization_CTE_MATERIALIZATION_MATERIALIZED
		case pg.CTEMaterialize_CTEMaterializeNever:
			mapped.Materialization = ir.CteMaterialization_CTE_MATERIALIZATION_NOT_MATERIALIZED
		default:
		}

		if entry.GetCtequery().GetSelectStmt() != nil {
			mapped.Query = m.selectStmt(entry.GetCtequery().GetSelectStmt())
		} else {
			mapped.Statement = m.statement(entry.GetCtequery())
		}

		result = append(result, mapped)
	}

	return result, value.GetRecursive()
}

func (m Mapper) insert(value *pg.InsertStmt) *ir.InsertStmt {
	result := &ir.InsertStmt{
		TableName: relationName(value.GetRelation()), Alias: value.GetRelation().GetAlias().GetAliasname(),
		Columns: targetNames(value.GetCols()), Returning: m.targets(value.GetReturningList()),
		OverridingSystem: value.GetOverride() == pg.OverridingKind_OVERRIDING_SYSTEM_VALUE,
	}
	result.With, result.WithRecursive = m.with(value.GetWithClause())
	source := value.GetSelectStmt().GetSelectStmt()

	switch {
	case source == nil:
		result.Source = &ir.InsertStmt_DefaultValues{DefaultValues: true}
	case len(source.GetValuesLists()) > 0 && source.GetWithClause() == nil && len(source.GetSortClause()) == 0 &&
		source.GetLimitCount() == nil && source.GetLimitOffset() == nil:
		result.Source = &ir.InsertStmt_Values{Values: m.values(source.GetValuesLists())}
	default:
		result.Source = &ir.InsertStmt_Query{Query: m.selectStmt(source)}
	}

	if value.GetOnConflictClause() != nil {
		result.OnConflict = m.conflict(value.GetOnConflictClause())
	}

	return result
}

func targetNames(nodes []*pg.Node) []string {
	result := make([]string, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.GetResTarget().GetName())
	}

	return result
}

func (m Mapper) assignments(nodes []*pg.Node) []*ir.Assignment {
	result := make([]*ir.Assignment, 0, len(nodes))

	for _, node := range nodes {
		target := node.GetResTarget()
		if multi := target.GetVal().GetMultiAssignRef(); multi != nil {
			if multi.GetColno() == 1 {
				result = append(result, &ir.Assignment{Columns: []string{target.GetName()}, Value: m.expr(multi.GetSource())})
			} else if len(result) > 0 {
				last := result[len(result)-1]
				last.Columns = append(last.GetColumns(), target.GetName())
			}
		} else {
			result = append(result, &ir.Assignment{Columns: []string{target.GetName()}, Value: m.expr(target.GetVal())})
		}
	}

	return result
}

func (m Mapper) conflict(value *pg.OnConflictClause) *ir.OnConflict {
	result := &ir.OnConflict{
		TargetConstraint: value.GetInfer().GetConname(),
		IndexPredicate:   m.expr(value.GetInfer().GetWhereClause()), Set: m.assignments(value.GetTargetList()),
		UpdateWhere: m.expr(value.GetWhereClause()),
	}
	for _, node := range value.GetInfer().GetIndexElems() {
		result.TargetColumns = append(result.TargetColumns, node.GetIndexElem().GetName())
	}

	switch value.GetAction() {
	case pg.OnConflictAction_ONCONFLICT_NOTHING:
		result.Action = ir.OnConflictAction_ON_CONFLICT_ACTION_DO_NOTHING
	case pg.OnConflictAction_ONCONFLICT_UPDATE:
		result.Action = ir.OnConflictAction_ON_CONFLICT_ACTION_DO_UPDATE
	default:
	}

	return result
}

func (m Mapper) update(value *pg.UpdateStmt) *ir.UpdateStmt {
	result := &ir.UpdateStmt{
		TableName: relationName(value.GetRelation()), Alias: value.GetRelation().GetAlias().GetAliasname(),
		Only: !value.GetRelation().GetInh(), Set: m.assignments(value.GetTargetList()),
		From:  m.fromItems(value.GetFromClause()),
		Where: m.expr(value.GetWhereClause()), Returning: m.targets(value.GetReturningList()),
	}
	result.With, result.WithRecursive = m.with(value.GetWithClause())

	return result
}

func (m Mapper) deleteStmt(value *pg.DeleteStmt) *ir.DeleteStmt {
	result := &ir.DeleteStmt{
		TableName: relationName(value.GetRelation()), Alias: value.GetRelation().GetAlias().GetAliasname(),
		Only: !value.GetRelation().GetInh(), Using: m.fromItems(value.GetUsingClause()),
		Where:     m.expr(value.GetWhereClause()),
		Returning: m.targets(value.GetReturningList()),
	}
	result.With, result.WithRecursive = m.with(value.GetWithClause())

	return result
}

func (m Mapper) merge(value *pg.MergeStmt) *ir.MergeStmt {
	result := &ir.MergeStmt{
		TargetName:  relationName(value.GetRelation()),
		TargetAlias: value.GetRelation().GetAlias().GetAliasname(), Source: m.from(value.GetSourceRelation()),
		On: m.expr(value.GetJoinCondition()), Returning: m.targets(value.GetReturningList()),
	}
	result.With, result.WithRecursive = m.with(value.GetWithClause())

	for _, node := range value.GetMergeWhenClauses() {
		when := node.GetMergeWhenClause()
		match := ir.MergeMatch_MERGE_MATCH_MATCHED

		switch when.GetMatchKind() {
		case pg.MergeMatchKind_MERGE_WHEN_NOT_MATCHED_BY_SOURCE:
			match = ir.MergeMatch_MERGE_MATCH_NOT_MATCHED_BY_SOURCE
		case pg.MergeMatchKind_MERGE_WHEN_NOT_MATCHED_BY_TARGET:
			match = ir.MergeMatch_MERGE_MATCH_NOT_MATCHED_BY_TARGET
		default:
		}

		result.Whens = append(result.Whens, &ir.MergeWhen{
			Match: match, Condition: m.expr(when.GetCondition()),
			Action: m.mergeAction(when),
		})
	}

	return result
}

func (m Mapper) mergeAction(value *pg.MergeWhenClause) *ir.MergeAction {
	result := &ir.MergeAction{OverridingSystem: value.GetOverride() == pg.OverridingKind_OVERRIDING_SYSTEM_VALUE}

	switch value.GetCommandType() {
	case pg.CmdType_CMD_INSERT:
		result.Kind = ir.MergeActionKind_MERGE_ACTION_KIND_INSERT
		result.InsertColumns = targetNames(value.GetTargetList())

		if len(value.GetValues()) == 0 {
			result.InsertValues = &ir.MergeAction_DefaultValues{DefaultValues: true}
		} else {
			result.InsertValues = &ir.MergeAction_Values{Values: &ir.Values{
				Rows: []*ir.ValuesRow{{Values: m.expressions(value.GetValues())}},
			}}
		}
	case pg.CmdType_CMD_UPDATE:
		result.Kind = ir.MergeActionKind_MERGE_ACTION_KIND_UPDATE
		result.Set = m.assignments(value.GetTargetList())
	case pg.CmdType_CMD_DELETE:
		result.Kind = ir.MergeActionKind_MERGE_ACTION_KIND_DELETE
	default:
		result.Kind = ir.MergeActionKind_MERGE_ACTION_KIND_DO_NOTHING
	}

	return result
}

func locking(value *pg.LockingClause) *ir.LockingClause {
	result := &ir.LockingClause{}

	switch value.GetStrength() {
	case pg.LockClauseStrength_LCS_FORUPDATE:
		result.Strength = ir.LockStrength_LOCK_STRENGTH_UPDATE
	case pg.LockClauseStrength_LCS_FORNOKEYUPDATE:
		result.Strength = ir.LockStrength_LOCK_STRENGTH_NO_KEY_UPDATE
	case pg.LockClauseStrength_LCS_FORSHARE:
		result.Strength = ir.LockStrength_LOCK_STRENGTH_SHARE
	case pg.LockClauseStrength_LCS_FORKEYSHARE:
		result.Strength = ir.LockStrength_LOCK_STRENGTH_KEY_SHARE
	default:
	}

	switch value.GetWaitPolicy() {
	case pg.LockWaitPolicy_LockWaitError:
		result.Wait = ir.LockWaitPolicy_LOCK_WAIT_POLICY_NOWAIT
	case pg.LockWaitPolicy_LockWaitSkip:
		result.Wait = ir.LockWaitPolicy_LOCK_WAIT_POLICY_SKIP_LOCKED
	default:
	}

	for _, node := range value.GetLockedRels() {
		result.OfTables = append(result.OfTables, tableObject(node.GetRangeVar()))
	}

	return result
}

func classify(node *pg.Node) ir.StatementKind {
	switch {
	case node.GetTruncateStmt() != nil:
		return ir.StatementKind_STATEMENT_KIND_TRUNCATE
	case node.GetCopyStmt() != nil:
		return ir.StatementKind_STATEMENT_KIND_COPY
	case node.GetTransactionStmt() != nil:
		return ir.StatementKind_STATEMENT_KIND_TCL
	case node.GetVacuumStmt() != nil, node.GetVariableSetStmt() != nil, node.GetExplainStmt() != nil,
		node.GetClusterStmt() != nil, node.GetReindexStmt() != nil:
		return ir.StatementKind_STATEMENT_KIND_UTILITY
	default:
		if node == nil || node.GetNode() == nil {
			return ir.StatementKind_STATEMENT_KIND_OTHER
		}

		field := node.ProtoReflect().WhichOneof(node.ProtoReflect().Descriptor().Oneofs().Get(0))

		name := string(field.Name())
		for _, prefix := range []string{
			"create_", "alter_", "drop_", "index_", "view_", "composite_", "grant_", "comment_", "rename_",
		} {
			if strings.HasPrefix(name, prefix) {
				return ir.StatementKind_STATEMENT_KIND_DDL
			}
		}

		return ir.StatementKind_STATEMENT_KIND_OTHER
	}
}
