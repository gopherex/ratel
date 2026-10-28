package query

import (
	"slices"
	"strconv"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
)

func (s *inference) expr(node *pg.Node, env *scope, expected *ir.TypeRef) *ir.QueryColumn {
	if node == nil || s.err != nil {
		return &ir.QueryColumn{Nullable: true}
	}

	switch {
	case node.GetAConst() != nil:
		return constant(node.GetAConst())
	case node.GetParamRef() != nil:
		return s.parameter(node.GetParamRef(), expected)
	case node.GetColumnRef() != nil:
		return s.column(stringsOf(node.GetColumnRef().GetFields()), env)
	case node.GetTypeCast() != nil:
		return s.cast(node.GetTypeCast(), env)
	case node.GetAExpr() != nil:
		return s.operation(node.GetAExpr(), env)
	case node.GetFuncCall() != nil:
		return s.call(node.GetFuncCall(), env)
	case node.GetCoalesceExpr() != nil:
		return s.coalesce(node.GetCoalesceExpr().GetArgs(), env)
	case node.GetCaseExpr() != nil:
		return s.caseExpr(node.GetCaseExpr(), env)
	case node.GetSubLink() != nil:
		return s.subquery(node.GetSubLink(), env)
	default:
		return s.special(node, env, expected)
	}
}

func (s *inference) cast(cast *pg.TypeCast, env *scope) *ir.QueryColumn {
	typ := s.resolveType(mapper.MapType(cast.GetTypeName()))

	var expected *ir.TypeRef

	arg := cast.GetArg()
	if arg.GetAArrayExpr() != nil ||
		(arg.GetParamRef() != nil && s.params[arg.GetParamRef().GetNumber()].GetType() == nil) {
		expected = typ
	}

	value := s.expr(arg, env, expected)

	name := value.GetName()
	if name == "" {
		name = typ.GetPgName()
	}

	return &ir.QueryColumn{Type: typ, Nullable: value.GetNullable(), Name: name}
}

func constant(value *pg.A_Const) *ir.QueryColumn {
	result := &ir.QueryColumn{Nullable: value.GetIsnull(), Type: scalar(unknownType)}

	switch {
	case value.GetIval() != nil:
		result.Type = scalar("int4")
	case value.GetFval() != nil:
		result.Type = scalar("numeric")
		if _, err := strconv.ParseInt(value.GetFval().GetFval(), 10, 64); err == nil {
			result.Type = scalar("int8")
		}
	case value.GetBoolval() != nil:
		result.Type = scalar("bool")
	default:
	}

	return result
}

func (s *inference) parameterValue(ref *pg.ParamRef) *ir.QueryParameter {
	number := ref.GetNumber()
	if number <= 0 {
		s.fail("invalid parameter number")

		return &ir.QueryParameter{}
	}

	if s.params[number] == nil {
		s.params[number] = &ir.QueryParameter{Number: uint32(number), Name: s.names[number]}
	}

	return s.params[number]
}

func (s *inference) parameter(ref *pg.ParamRef, expected *ir.TypeRef) *ir.QueryColumn {
	value := s.parameterValue(ref)
	typ, seen := s.refTypes[ref]

	if !seen {
		typ = value.GetType()
		s.refTypes[ref] = typ
		s.refs = append(s.refs, ref)
	}

	if !unknown(expected) {
		inconsistent := typ == nil && value.GetType() != nil && typeKey(value.GetType()) != typeKey(expected)
		if inconsistent || (typ != nil && !s.signatures.coercible(typ, expected)) {
			s.fail("conflicting parameter $" + strconv.Itoa(int(ref.GetNumber())))
		}

		if value.GetType() == nil {
			value.Type = proto.CloneOf(expected)
		}

		if typ == nil {
			typ = value.GetType()
			s.refTypes[ref] = typ
		}
	}

	s.parameterAnnotations(value)

	return &ir.QueryColumn{Type: typ, Nullable: value.GetNullable()}
}

func (s *inference) context(node *pg.Node, env *scope, expected *ir.QueryColumn) *ir.QueryColumn {
	var typ *ir.TypeRef

	ref := node.GetParamRef()
	refType, seen := s.refTypes[ref]

	unbound := ref != nil && ((seen && refType == nil) || (!seen && s.params[ref.GetNumber()].GetType() == nil))
	if node.GetSetToDefault() != nil || unbound {
		typ = expected.GetType()
	}

	value := s.expr(node, env, typ)
	if expected.GetSourceColumn() == nil {
		return value
	}

	param := s.parameterContext(node, expected)
	if param != nil {
		value.Nullable = param.GetNullable()
	}

	return value
}

func (s *inference) parameterContext(node *pg.Node, expected *ir.QueryColumn) *ir.QueryParameter {
	for node.GetTypeCast() != nil {
		node = node.GetTypeCast().GetArg()
	}

	if node.GetParamRef() == nil {
		return nil
	}

	number := node.GetParamRef().GetNumber()

	param := s.params[number]
	if param == nil {
		return nil
	}

	if param.GetColumn() == nil {
		param.Column = proto.CloneOf(expected.GetSourceColumn())
	}

	if s.contextual[number] {
		param.Nullable = param.GetNullable() && expected.GetNullable()
	} else {
		param.Nullable = expected.GetNullable()
	}

	s.contextual[number] = true
	s.parameterAnnotations(param)

	return param
}

func (s *inference) expressions(nodes []*pg.Node, env *scope, expected *ir.TypeRef) []*ir.QueryColumn {
	result := make([]*ir.QueryColumn, len(nodes))
	for index, node := range nodes {
		result[index] = s.expr(node, env, expected)
	}

	return result
}

func (s *inference) constrain(nodes []*pg.Node, args []*ir.QueryColumn, types []*ir.TypeRef, env *scope) {
	for index, node := range nodes {
		if index >= len(types) {
			break
		}

		s.expr(node, env, types[index])

		for _, arg := range args {
			if arg.GetSourceColumn() != nil {
				s.parameterContext(node, arg)

				break
			}
		}
	}
}

func (s *inference) call(call *pg.FuncCall, env *scope) *ir.QueryColumn {
	nodes := call.GetArgs()
	if call.GetAggWithinGroup() {
		nodes = slices.Clone(nodes)
		for _, order := range call.GetAggOrder() {
			nodes = append(nodes, order.GetSortBy().GetNode())
		}
	}

	args := s.expressions(nodes, env, nil)
	names := stringsOf(call.GetFuncname())
	chosen := s.function(names, args, call.GetFuncVariadic())
	s.constrain(nodes, nil, chosen.inputs, env)
	s.expr(call.GetAggFilter(), env, scalar("bool"))

	for _, node := range call.GetAggOrder() {
		s.ordering(node.GetSortBy().GetNode(), env)
	}

	if call.GetOver() != nil {
		env.windows = append(env.windows, call.GetOver())
	}

	name := names[len(names)-1]
	nullable := true
	parts := chosen.function.GetName().GetParts()

	if len(parts) > 0 && parts[0] == "pg_catalog" {
		nullable = builtinNullable(name, chosen.function, args)
	}

	return &ir.QueryColumn{Name: name, Type: proto.CloneOf(chosen.result), Nullable: nullable}
}

func builtinNullable(name string, function *ir.Function, args []*ir.QueryColumn) bool {
	switch name {
	case "count", "row_number", "rank", "dense_rank", "percent_rank", "cume_dist":
		return false
	case "abs", "lower", "upper", "length", "char_length", "octet_length", "btrim", "ltrim", "rtrim", "ntile":
		for _, arg := range args {
			if arg.GetNullable() {
				return true
			}
		}

		return false
	default:
		if function.GetRoutineKind() == "a" || function.GetRoutineKind() == "w" {
			return true
		}

		return true
	}
}

func (s *inference) window(window *pg.WindowDef, env *scope) {
	s.grouping(window.GetPartitionClause(), env)

	orders := s.windowOrders(window, env)
	for _, node := range orders {
		s.ordering(node.GetSortBy().GetNode(), env)
	}

	s.frameOffset(window.GetStartOffset(), window, orders, env)
	s.frameOffset(window.GetEndOffset(), window, orders, env)
}

func (s *inference) windowOrders(window *pg.WindowDef, env *scope) []*pg.Node {
	orders := window.GetOrderClause()

	name := window.GetRefname()
	if name == "" && window.GetName() != "" && env.windowDefs[window.GetName()] != window {
		name = window.GetName()
	}

	seen := make(map[string]bool)

	for len(orders) == 0 && name != "" {
		definition := env.windowDefs[name]
		if seen[name] || definition == nil {
			s.fail("window " + name)

			return nil
		}

		seen[name] = true
		orders = definition.GetOrderClause()
		name = definition.GetRefname()
	}

	return orders
}

func (s *inference) frameOffset(node *pg.Node, window *pg.WindowDef, orders []*pg.Node, env *scope) {
	if node == nil {
		return
	}

	typ := scalar("int8")
	// PostgreSQL parsenodes.h: FRAMEOPTION_RANGE is a syntax bit, not a tuning value.
	const frameRange = 0x00002
	if window.GetFrameOptions()&frameRange != 0 {
		if len(orders) != 1 {
			s.fail("RANGE frame ORDER BY")

			return
		}

		actual := s.expr(node, env, nil)
		order := s.expr(orders[0].GetSortBy().GetNode(), env, nil)
		typ = s.rangeOffset(s.signatures.baseType(order.GetType()), actual.GetType())
	}

	s.expr(node, env, typ)
}

func (s *inference) rangeOffset(order, actual *ir.TypeRef) *ir.TypeRef {
	var result *ir.TypeRef

	preferred := actual
	if unknown(preferred) {
		preferred = order
	} else {
		preferred = s.signatures.baseType(preferred)
	}

	ambiguous := false

	const rangeArguments = 5

	for _, function := range s.signatures.functions["pg_catalog.in_range"] {
		args := function.GetArguments()
		if len(args) != rangeArguments || typeKey(args[0].GetType()) != typeKey(order) {
			continue
		}

		typ := args[2].GetType()
		if !s.signatures.coercible(actual, typ) {
			continue
		}

		if typeKey(typ) == typeKey(preferred) {
			return typ
		}

		if result != nil && typeKey(result) != typeKey(typ) {
			ambiguous = true
		}

		result = typ
	}

	if result == nil || ambiguous {
		s.fail("RANGE frame offset for " + typeKey(order))

		return nil
	}

	return result
}

func (s *inference) operation(expr *pg.A_Expr, env *scope) *ir.QueryColumn {
	name := strings.TrimPrefix(strings.Join(stringsOf(expr.GetName()), "."), "pg_catalog.")
	if expr.GetKind() == pg.A_Expr_Kind_AEXPR_OP_ANY || expr.GetKind() == pg.A_Expr_Kind_AEXPR_OP_ALL {
		return s.anyOperation(expr, env, name)
	}

	if expr.GetRexpr().GetList() != nil {
		return s.listOperation(expr, env, name)
	}

	nodes := []*pg.Node{expr.GetRexpr()}
	if expr.GetLexpr() != nil {
		nodes = []*pg.Node{expr.GetLexpr(), expr.GetRexpr()}
	}

	args := s.expressions(nodes, env, nil)
	chosen := s.operator(name, args)
	s.constrain(nodes, args, chosen.inputs, env)

	nullable := false
	for _, arg := range args {
		nullable = nullable || arg.GetNullable()
	}

	if expr.GetKind() == pg.A_Expr_Kind_AEXPR_DISTINCT || expr.GetKind() == pg.A_Expr_Kind_AEXPR_NOT_DISTINCT {
		nullable = false
	}

	if expr.GetKind() == pg.A_Expr_Kind_AEXPR_NULLIF {
		return &ir.QueryColumn{Name: "nullif", Type: args[0].GetType(), Nullable: true}
	}

	if strings.Contains(name, "->") || strings.Contains(name, "#>") ||
		(name == "@@" && args[0].GetType().GetPgName() == "jsonb") {
		nullable = true
	}

	return &ir.QueryColumn{Type: proto.CloneOf(chosen.result), Nullable: nullable}
}

func (s *inference) anyOperation(expr *pg.A_Expr, env *scope, name string) *ir.QueryColumn {
	left := s.expr(expr.GetLexpr(), env, nil)

	var expected *ir.TypeRef

	if !unknown(left.GetType()) {
		expected = array(left.GetType())
	}

	right := s.expr(expr.GetRexpr(), env, expected)
	element := &ir.QueryColumn{Type: right.GetType().GetElement(), Nullable: true}

	chosen := s.operator(name, []*ir.QueryColumn{left, element})
	if len(chosen.inputs) == binaryArity {
		s.expr(expr.GetLexpr(), env, chosen.inputs[0])
		s.expr(expr.GetRexpr(), env, array(chosen.inputs[1]))
	}

	return &ir.QueryColumn{Type: scalar("bool"), Nullable: true}
}

func (s *inference) listOperation(expr *pg.A_Expr, env *scope, name string) *ir.QueryColumn {
	left := s.expr(expr.GetLexpr(), env, nil)
	values := expr.GetRexpr().GetList().GetItems()

	for _, value := range values {
		right := s.expr(value, env, left.GetType())

		operator := name
		if strings.Contains(name, "BETWEEN") {
			operator = "="
		}

		chosen := s.operator(operator, []*ir.QueryColumn{left, right})
		s.constrain([]*pg.Node{expr.GetLexpr(), value}, []*ir.QueryColumn{left, right}, chosen.inputs, env)
	}

	return &ir.QueryColumn{Type: scalar("bool"), Nullable: true}
}

func (s *inference) coalesce(nodes []*pg.Node, env *scope) *ir.QueryColumn {
	values := s.expressions(nodes, env, nil)
	typ := s.common(values)
	s.expressions(nodes, env, typ)

	nullable := true
	for _, value := range values {
		nullable = nullable && value.GetNullable()
	}

	return &ir.QueryColumn{Name: "coalesce", Type: typ, Nullable: nullable}
}

func (s *inference) caseExpr(expr *pg.CaseExpr, env *scope) *ir.QueryColumn {
	operand := s.expr(expr.GetArg(), env, nil)
	if expr.GetArg() != nil && unknown(operand.GetType()) {
		s.expr(expr.GetArg(), env, scalar("text"))
		operand.Type = scalar("text")
	}

	nodes := make([]*pg.Node, 1, 1+len(expr.GetArgs()))
	values := make([]*ir.QueryColumn, 1, 1+len(expr.GetArgs()))
	nodes[0] = expr.GetDefresult()

	for _, node := range expr.GetArgs() {
		when := node.GetCaseWhen()
		if expr.GetArg() != nil {
			right := s.expr(when.GetExpr(), env, nil)
			chosen := s.operator("=", []*ir.QueryColumn{operand, right})
			s.constrain([]*pg.Node{expr.GetArg(), when.GetExpr()}, []*ir.QueryColumn{operand, right}, chosen.inputs, env)
		} else {
			s.expr(when.GetExpr(), env, scalar("bool"))
		}

		nodes = append(nodes, when.GetResult())
		values = append(values, s.expr(when.GetResult(), env, nil))
	}

	values[0] = s.expr(expr.GetDefresult(), env, nil)
	typ := s.common(values)
	s.expressions(nodes, env, typ)

	nullable := false
	for _, value := range values {
		nullable = nullable || value.GetNullable()
	}

	return &ir.QueryColumn{Name: "case", Type: typ, Nullable: nullable}
}

func (s *inference) subquery(link *pg.SubLink, env *scope) *ir.QueryColumn {
	columns := s.statement(link.GetSubselect(), newScope(env))

	if link.GetSubLinkType() == pg.SubLinkType_EXISTS_SUBLINK {
		return &ir.QueryColumn{Type: scalar("bool")}
	}

	if len(columns) != 1 {
		s.fail("subquery result width")

		return &ir.QueryColumn{}
	}

	col := columns[0]

	switch link.GetSubLinkType() {
	case pg.SubLinkType_EXPR_SUBLINK:
		col.Nullable = true
		col.SourceColumn = nil

		return col
	case pg.SubLinkType_ARRAY_SUBLINK:
		return &ir.QueryColumn{Name: "array", Type: array(col.GetType())}
	case pg.SubLinkType_ANY_SUBLINK, pg.SubLinkType_ALL_SUBLINK:
		s.expr(link.GetTestexpr(), env, col.GetType())

		return &ir.QueryColumn{Type: scalar("bool"), Nullable: true}
	default:
		s.fail("subquery expression " + link.String())

		return &ir.QueryColumn{}
	}
}
