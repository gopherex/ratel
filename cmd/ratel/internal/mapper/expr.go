package mapper

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"

	"github.com/gopherex/ratel/ir"
)

// MapExpr maps an expression and assigns deterministic ids to its entire tree.
// An absent AST expression stays absent, rather than becoming a SQL NULL.
func (m Mapper) MapExpr(node *pg.Node) *ir.Expr {
	result := m.expr(node)
	if result != nil {
		m.finish(result)
	}

	return result
}

func (m Mapper) expr(node *pg.Node) *ir.Expr {
	if node == nil {
		return nil
	}

	switch {
	case node.GetAConst() != nil:
		return m.literal(node.GetAConst())
	case node.GetColumnRef() != nil:
		return columnRef(node.GetColumnRef())
	case node.GetParamRef() != nil:
		return &ir.Expr{Node: &ir.Expr_Parameter{Parameter: &ir.ParameterRef{
			Position: boundedUint(int(node.GetParamRef().GetNumber())),
		}}}
	case node.GetFuncCall() != nil:
		return m.functionCall(node.GetFuncCall())
	case node.GetAExpr() != nil:
		value := node.GetAExpr()

		return m.operator(operatorSymbol(value), value.GetLexpr(), value.GetRexpr())
	case node.GetBoolExpr() != nil:
		value := node.GetBoolExpr()
		symbol := strings.TrimSuffix(value.GetBoolop().String(), "_EXPR")

		return m.operator(symbol, value.GetArgs()...)
	case node.GetNullTest() != nil:
		value := node.GetNullTest()
		symbol := "IS NULL"

		if value.GetNulltesttype() == pg.NullTestType_IS_NOT_NULL {
			symbol = "IS NOT NULL"
		}

		return m.operator(symbol, value.GetArg())
	case node.GetBooleanTest() != nil:
		value := node.GetBooleanTest()
		symbol := strings.ReplaceAll(value.GetBooltesttype().String(), "_", " ")

		return m.operator(symbol, value.GetArg())
	default:
		return m.compoundExpr(node)
	}
}

func (m Mapper) compoundExpr(node *pg.Node) *ir.Expr {
	switch {
	case node.GetTypeCast() != nil:
		value := node.GetTypeCast()

		return &ir.Expr{Node: &ir.Expr_Cast{Cast: &ir.CastExpr{
			Expr: m.expr(value.GetArg()), TargetType: MapType(value.GetTypeName()),
		}}}
	case node.GetCaseExpr() != nil:
		return m.caseExpr(node.GetCaseExpr())
	case node.GetList() != nil:
		return m.list(node.GetList().GetItems(), false)
	case node.GetRowExpr() != nil:
		return m.list(node.GetRowExpr().GetArgs(), true)
	case node.GetSubLink() != nil:
		return m.subquery(node)
	case node.GetCoalesceExpr() != nil:
		return m.namedCall("coalesce", node.GetCoalesceExpr().GetArgs())
	case node.GetMinMaxExpr() != nil:
		value := node.GetMinMaxExpr()
		name := "greatest"

		if value.GetOp() == pg.MinMaxOp_IS_LEAST {
			name = "least"
		}

		return m.namedCall(name, value.GetArgs())
	case node.GetAStar() != nil:
		return &ir.Expr{Node: &ir.Expr_Star{Star: &ir.StarExpr{}}}
	default:
		return &ir.Expr{Node: &ir.Expr_RawSql{RawSql: m.expressionSQL(node)}}
	}
}

func (m Mapper) literal(value *pg.A_Const) *ir.Expr {
	result := &ir.Literal{}

	switch {
	case value.GetIsnull():
		result.Value = &ir.Literal_NullValue{NullValue: true}
	case value.GetIval() != nil:
		result.Value = &ir.Literal_IntValue{IntValue: int64(value.GetIval().GetIval())}
	case value.GetFval() != nil:
		result.Value = &ir.Literal_NumericValue{NumericValue: value.GetFval().GetFval()}
	case value.GetSval() != nil:
		result.Value = &ir.Literal_StringValue{StringValue: value.GetSval().GetSval()}
	case value.GetBoolval() != nil:
		result.Value = &ir.Literal_BoolValue{BoolValue: value.GetBoolval().GetBoolval()}
	default:
		return &ir.Expr{Node: &ir.Expr_RawSql{RawSql: m.expressionSQL(&pg.Node{
			Node: &pg.Node_AConst{AConst: value},
		})}}
	}

	return &ir.Expr{Node: &ir.Expr_Literal{Literal: result}}
}

func columnRef(value *pg.ColumnRef) *ir.Expr {
	fields := value.GetFields()
	if len(fields) == 0 {
		return nil
	}

	last := fields[len(fields)-1]
	qualifier := quoteParts(stringsOf(fields[:len(fields)-1]))

	if last.GetAStar() != nil {
		return &ir.Expr{Node: &ir.Expr_Star{Star: &ir.StarExpr{Qualifier: qualifier}}}
	}

	return &ir.Expr{Node: &ir.Expr_ColumnRef{ColumnRef: &ir.ColumnRef{
		Qualifier: qualifier, Column: last.GetString_().GetSval(),
	}}}
}

func (m Mapper) expressions(nodes []*pg.Node) []*ir.Expr {
	result := make([]*ir.Expr, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, m.expr(node))
	}

	return result
}

func (m Mapper) operator(symbol string, nodes ...*pg.Node) *ir.Expr {
	operands := make([]*ir.Expr, 0, len(nodes))

	for _, node := range nodes {
		if node != nil {
			operands = append(operands, m.expr(node))
		}
	}

	return &ir.Expr{Node: &ir.Expr_Operator{Operator: &ir.OperatorExpr{Symbol: symbol, Operands: operands}}}
}

func operatorSymbol(value *pg.A_Expr) string {
	symbol := dotted(value.GetName())
	kind := value.GetKind()

	switch kind {
	case pg.A_Expr_Kind_AEXPR_IN:
		if symbol == "<>" {
			return "NOT IN"
		}

		return "IN"
	case pg.A_Expr_Kind_AEXPR_LIKE, pg.A_Expr_Kind_AEXPR_ILIKE, pg.A_Expr_Kind_AEXPR_SIMILAR:
		name := strings.TrimPrefix(kind.String(), "AEXPR_")
		if kind == pg.A_Expr_Kind_AEXPR_SIMILAR {
			name = "SIMILAR TO"
		}

		if strings.HasPrefix(symbol, "!") {
			name = "NOT " + name
		}

		return name
	case pg.A_Expr_Kind_AEXPR_DISTINCT:
		return "IS DISTINCT FROM"
	case pg.A_Expr_Kind_AEXPR_NOT_DISTINCT:
		return "IS NOT DISTINCT FROM"
	case pg.A_Expr_Kind_AEXPR_OP_ANY:
		return symbol + " ANY"
	case pg.A_Expr_Kind_AEXPR_OP_ALL:
		return symbol + " ALL"
	case pg.A_Expr_Kind_AEXPR_BETWEEN, pg.A_Expr_Kind_AEXPR_NOT_BETWEEN,
		pg.A_Expr_Kind_AEXPR_BETWEEN_SYM, pg.A_Expr_Kind_AEXPR_NOT_BETWEEN_SYM:
		name := strings.ReplaceAll(strings.TrimPrefix(kind.String(), "AEXPR_"), "_", " ")

		return strings.ReplaceAll(name, "SYM", "SYMMETRIC")
	case pg.A_Expr_Kind_AEXPR_NULLIF:
		return "NULLIF"
	default:
		return symbol
	}
}

func (m Mapper) functionCall(value *pg.FuncCall) *ir.Expr {
	args := m.expressions(value.GetArgs())
	if value.GetAggStar() {
		args = []*ir.Expr{{Node: &ir.Expr_Star{Star: &ir.StarExpr{}}}}
	}

	return &ir.Expr{Node: &ir.Expr_FunctionCall{FunctionCall: &ir.FunctionCall{
		Name: qualified(value.GetFuncname()), Arguments: args, Distinct: value.GetAggDistinct(),
		Variadic: value.GetFuncVariadic(), OrderBy: m.orders(value.GetAggOrder()),
		Filter: m.expr(value.GetAggFilter()), Over: m.window(value.GetOver()),
	}}}
}

func (m Mapper) namedCall(name string, args []*pg.Node) *ir.Expr {
	return &ir.Expr{Node: &ir.Expr_FunctionCall{FunctionCall: &ir.FunctionCall{
		Name: &ir.QualifiedName{Parts: []string{name}}, Arguments: m.expressions(args),
	}}}
}

func (m Mapper) list(nodes []*pg.Node, row bool) *ir.Expr {
	return &ir.Expr{Node: &ir.Expr_List{List: &ir.ListExpr{Elements: m.expressions(nodes), IsRow: row}}}
}

func (m Mapper) caseExpr(value *pg.CaseExpr) *ir.Expr {
	result := &ir.CaseExpr{Operand: m.expr(value.GetArg()), ElseResult: m.expr(value.GetDefresult())}

	for _, node := range value.GetArgs() {
		when := node.GetCaseWhen()
		result.Whens = append(result.Whens, &ir.CaseWhen{Condition: m.expr(when.GetExpr()), Result: m.expr(when.GetResult())})
	}

	return &ir.Expr{Node: &ir.Expr_CaseExpr{CaseExpr: result}}
}

func (m Mapper) subquery(node *pg.Node) *ir.Expr {
	value := node.GetSubLink()

	var kind ir.SubqueryKind

	switch value.GetSubLinkType() {
	case pg.SubLinkType_EXPR_SUBLINK:
		kind = ir.SubqueryKind_SUBQUERY_KIND_SCALAR
	case pg.SubLinkType_EXISTS_SUBLINK:
		kind = ir.SubqueryKind_SUBQUERY_KIND_EXISTS
	case pg.SubLinkType_ALL_SUBLINK:
		kind = ir.SubqueryKind_SUBQUERY_KIND_ALL
	case pg.SubLinkType_ANY_SUBLINK:
		kind = ir.SubqueryKind_SUBQUERY_KIND_ANY
		if len(value.GetOperName()) == 0 {
			kind = ir.SubqueryKind_SUBQUERY_KIND_IN
		}
	default:
		return &ir.Expr{Node: &ir.Expr_RawSql{RawSql: m.expressionSQL(node)}}
	}

	return &ir.Expr{Node: &ir.Expr_Subquery{Subquery: &ir.SubqueryExpr{
		Kind:  kind,
		Query: m.selectStmt(value.GetSubselect().GetSelectStmt()), Operand: m.expr(value.GetTestexpr()),
		Symbol: dotted(value.GetOperName()),
	}}}
}

func (m Mapper) expressionSQL(node *pg.Node) string {
	target := &pg.Node{Node: &pg.Node_ResTarget{ResTarget: &pg.ResTarget{Val: node}}}
	selectNode := &pg.Node{Node: &pg.Node_SelectStmt{SelectStmt: &pg.SelectStmt{TargetList: []*pg.Node{target}}}}

	sql, err := pgquery.Deparse(&pg.ParseResult{Stmts: []*pg.RawStmt{{Stmt: selectNode}}})
	if err != nil {
		return m.sql[m.start:m.end]
	}

	return strings.TrimPrefix(sql, "SELECT ")
}
