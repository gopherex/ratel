package query

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/pkg/ir"
)

func (s *inference) special(node *pg.Node, env *scope, expected *ir.TypeRef) *ir.QueryColumn {
	switch {
	case node.GetBoolExpr() != nil:
		return s.boolean(node.GetBoolExpr(), env)
	case node.GetNullTest() != nil:
		s.expr(node.GetNullTest().GetArg(), env, nil)

		return &ir.QueryColumn{Type: scalar("bool")}
	case node.GetBooleanTest() != nil:
		s.expr(node.GetBooleanTest().GetArg(), env, scalar("bool"))

		return &ir.QueryColumn{Type: scalar("bool")}
	case node.GetAArrayExpr() != nil:
		values := s.expressions(node.GetAArrayExpr().GetElements(), env, expected.GetElement())
		typ := s.common(values)
		s.expressions(node.GetAArrayExpr().GetElements(), env, typ)

		return &ir.QueryColumn{Name: "array", Type: array(typ)}
	case node.GetMinMaxExpr() != nil:
		return s.minmax(node.GetMinMaxExpr(), env)
	case node.GetRowExpr() != nil:
		s.expressions(node.GetRowExpr().GetArgs(), env, nil)

		return &ir.QueryColumn{Type: &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_PSEUDO, PgName: "record"}}
	case node.GetGroupingSet() != nil:
		s.expressions(node.GetGroupingSet().GetContent(), env, nil)

		return &ir.QueryColumn{Type: scalar("int4")}
	case node.GetGroupingFunc() != nil:
		s.expressions(node.GetGroupingFunc().GetArgs(), env, nil)

		return &ir.QueryColumn{Name: "grouping", Type: scalar("int4")}
	case node.GetCollateClause() != nil:
		value := s.expr(node.GetCollateClause().GetArg(), env, expected)
		value.SourceColumn = nil

		return value
	case node.GetSqlvalueFunction() != nil:
		return sqlValue(node.GetSqlvalueFunction())
	case node.GetAIndirection() != nil:
		return s.indirection(node.GetAIndirection(), env)
	case node.GetSetToDefault() != nil:
		return &ir.QueryColumn{Type: expected, Nullable: true}
	default:
		s.fail("expression " + node.String())

		return &ir.QueryColumn{}
	}
}

func (s *inference) minmax(expr *pg.MinMaxExpr, env *scope) *ir.QueryColumn {
	value := s.coalesce(expr.GetArgs(), env)
	value.Name = "greatest"

	if expr.GetOp() == pg.MinMaxOp_IS_LEAST {
		value.Name = "least"
	}

	return value
}

func (s *inference) boolean(expr *pg.BoolExpr, env *scope) *ir.QueryColumn {
	args := s.expressions(expr.GetArgs(), env, scalar("bool"))
	nullable := false

	for _, arg := range args {
		nullable = nullable || arg.GetNullable()
	}

	return &ir.QueryColumn{Type: scalar("bool"), Nullable: nullable}
}

func (s *inference) ordering(node *pg.Node, env *scope) {
	value := s.expr(node, env, nil)
	if unknown(value.GetType()) && node.GetParamRef() != nil {
		s.parameter(node.GetParamRef(), scalar("text"))
	}
}

func (s *inference) grouping(nodes []*pg.Node, env *scope) {
	for _, node := range nodes {
		switch {
		case node.GetGroupingSet() != nil:
			s.grouping(node.GetGroupingSet().GetContent(), env)
		case node.GetList() != nil:
			s.grouping(node.GetList().GetItems(), env)
		case node.GetRowExpr() != nil && node.GetRowExpr().GetRowFormat() == pg.CoercionForm_COERCE_IMPLICIT_CAST:
			s.grouping(node.GetRowExpr().GetArgs(), env)
		default:
			s.ordering(node, env)
		}
	}
}

func sqlValue(value *pg.SQLValueFunction) *ir.QueryColumn {
	typ := "name"

	switch value.GetOp() {
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_DATE:
		typ = "date"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_TIME, pg.SQLValueFunctionOp_SVFOP_CURRENT_TIME_N:
		typ = "timetz"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP, pg.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP_N:
		typ = "timestamptz"
	case pg.SQLValueFunctionOp_SVFOP_LOCALTIME, pg.SQLValueFunctionOp_SVFOP_LOCALTIME_N:
		typ = "time"
	case pg.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP, pg.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP_N:
		typ = "timestamp"
	default:
	}

	name := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(value.GetOp().String(), "SVFOP_"), "_N"))

	return &ir.QueryColumn{Name: name, Type: scalar(typ)}
}

func markGroupingNullable(nodes []*pg.Node, env *scope) {
	for _, node := range nodes {
		if node.GetGroupingSet() != nil {
			for _, col := range append(env.columns, env.qualified...) {
				col.Nullable = true
			}

			return
		}
	}
}

func (s *inference) indirection(value *pg.A_Indirection, env *scope) *ir.QueryColumn {
	result := s.expr(value.GetArg(), env, nil)

	for _, node := range value.GetIndirection() {
		index := node.GetAIndices()
		if index == nil {
			s.fail("field expression " + node.String())

			continue
		}

		s.expr(index.GetLidx(), env, scalar("int4"))
		s.expr(index.GetUidx(), env, scalar("int4"))

		if !index.GetIsSlice() {
			result.Type = result.GetType().GetElement()
		}
	}

	result.Nullable = true
	result.SourceColumn = nil

	return result
}
