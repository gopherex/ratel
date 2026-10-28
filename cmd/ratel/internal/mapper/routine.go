package mapper

import (
	"cmp"
	"slices"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"
)

// MapCreateDomain preserves domain declarations without resolving their base type.
func (m Mapper) MapCreateDomain(value *pg.CreateDomainStmt) *ir.DomainType {
	if value == nil {
		return nil
	}

	result := &ir.DomainType{
		Name: qualified(value.GetDomainname()), BaseType: MapType(value.GetTypeName()),
		Nullable: true, Collation: dotted(value.GetCollClause().GetCollname()),
	}

	for _, node := range value.GetConstraints() {
		entry := node.GetConstraint()
		switch entry.GetContype() {
		case pg.ConstrType_CONSTR_NOTNULL:
			result.Nullable = false
		case pg.ConstrType_CONSTR_DEFAULT:
			result.DefaultExpr = m.expr(entry.GetRawExpr())
		case pg.ConstrType_CONSTR_CHECK:
			result.Constraints = append(result.Constraints, &ir.DomainConstraint{
				Name: entry.GetConname(), Check: m.expr(entry.GetRawExpr()),
				Meta: &ir.Metadata{Source: m.span(int(entry.GetLocation()), m.end)},
			})
		default:
		}
	}

	slices.SortStableFunc(result.GetConstraints(), func(left, right *ir.DomainConstraint) int {
		return cmp.Compare(left.GetName(), right.GetName())
	})
	m.finish(result)

	return result
}

// MapCompositeType preserves field order and qualified collations.
func (m Mapper) MapCompositeType(value *pg.CompositeTypeStmt) *ir.CompositeType {
	if value == nil {
		return nil
	}

	result := &ir.CompositeType{Name: relationName(value.GetTypevar())}

	for _, node := range value.GetColdeflist() {
		field := node.GetColumnDef()
		result.Fields = append(result.Fields, &ir.CompositeField{
			Name: field.GetColname(),
			Type: MapType(field.GetTypeName()), Collation: dotted(field.GetCollClause().GetCollname()),
			Meta: &ir.Metadata{Source: m.span(int(field.GetLocation()), m.end)},
		})
	}

	m.finish(result)

	return result
}

// MapCreateRange retains explicitly named support functions and multirange type.
// Implicit multirange names are catalog construction, not syntax mapping.
func (m Mapper) MapCreateRange(value *pg.CreateRangeStmt) *ir.RangeType {
	if value == nil {
		return nil
	}

	result := &ir.RangeType{Name: qualified(value.GetTypeName())}

	for _, node := range value.GetParams() {
		entry := node.GetDefElem()
		names := entry.GetArg().GetTypeName().GetNames()

		switch entry.GetDefname() {
		case "subtype":
			result.Subtype = MapType(entry.GetArg().GetTypeName())
		case "subtype_opclass":
			result.SubtypeOpclass = dotted(names)
		case "collation":
			result.Collation = dotted(names)
		case "canonical":
			result.Canonical = dotted(names)
		case "subtype_diff":
			result.SubtypeDiff = dotted(names)
		case "multirange_type_name":
			result.Multirange = dotted(names)
		}
	}

	m.finish(result)

	return result
}

// MapCreateFunction returns exactly one routine for a non-nil declaration.
// Identity names append the input argument signature as required by QualifiedName.
func (m Mapper) MapCreateFunction(value *pg.CreateFunctionStmt) (*ir.Function, *ir.Procedure) {
	if value == nil {
		return nil, nil
	}

	arguments := m.arguments(value.GetParameters())
	name := qualified(value.GetFuncname())
	signature := make([]string, 0, len(arguments))

	var defaults uint32

	var variadic *ir.TypeRef

	for _, argument := range arguments {
		if argument.GetMode() != ir.ArgMode_ARG_MODE_OUT {
			if argument.GetDefaultValue() != nil {
				defaults++
			}

			if argument.GetMode() == ir.ArgMode_ARG_MODE_VARIADIC {
				variadic = proto.CloneOf(argument.GetType().GetElement())
			}

			signature = append(signature, typeIdentity(argument.GetType()))
		}
	}

	name.Parts = append(name.GetParts(), strings.Join(signature, ","))
	function := &ir.Function{
		Name: name, Arguments: arguments, Returns: returnType(value),
		Volatility: ir.Volatility_VOLATILITY_VOLATILE, NullInput: ir.NullInputBehavior_NULL_INPUT_BEHAVIOR_CALLED,
		Parallel:         ir.ParallelSafety_PARALLEL_SAFETY_UNSAFE,
		DefaultArguments: defaults, VariadicElement: variadic,
	}
	m.functionOptions(function, value.GetOptions())

	function.SqlBody = m.sqlBody(value.GetSqlBody())
	if value.GetSqlBody() != nil {
		function.Body = m.statementSQL(&pg.Node{Node: &pg.Node_CreateFunctionStmt{CreateFunctionStmt: value}})
	}

	if value.GetIsProcedure() {
		procedure := &ir.Procedure{
			Name: name, Arguments: arguments, Language: function.GetLanguage(),
			SecurityDefiner: function.GetSecurityDefiner(), Body: function.GetBody(), SqlBody: function.GetSqlBody(),
		}
		m.finish(procedure)

		return nil, procedure
	}

	m.finish(function)

	return function, nil
}

func typeIdentity(value *ir.TypeRef) string {
	if value.GetKind() == ir.TypeKind_TYPE_KIND_ARRAY {
		return typeIdentity(value.GetElement()) + "[]"
	}

	if value.GetUdt() != nil {
		return quoteParts(value.GetUdt().GetName().GetParts())
	}

	return value.GetPgName()
}

func quoteParts(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, quoteIdentifier(part))
	}

	return strings.Join(quoted, ".")
}

func quoteIdentifier(value string) string {
	simple := value != ""

	for index, char := range value {
		letter := char >= 'a' && char <= 'z' || char == '_'
		suffix := index > 0 && (char >= '0' && char <= '9' || char == '$')

		if !letter && !suffix {
			simple = false

			break
		}
	}

	if simple {
		return value
	}

	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (m Mapper) arguments(nodes []*pg.Node) []*ir.Argument {
	result := make([]*ir.Argument, 0, len(nodes))

	for _, node := range nodes {
		value := node.GetFunctionParameter()
		mode := ir.ArgMode_ARG_MODE_IN

		switch value.GetMode() {
		case pg.FunctionParameterMode_FUNC_PARAM_OUT, pg.FunctionParameterMode_FUNC_PARAM_TABLE:
			mode = ir.ArgMode_ARG_MODE_OUT
		case pg.FunctionParameterMode_FUNC_PARAM_INOUT:
			mode = ir.ArgMode_ARG_MODE_INOUT
		case pg.FunctionParameterMode_FUNC_PARAM_VARIADIC:
			mode = ir.ArgMode_ARG_MODE_VARIADIC
		default:
		}

		result = append(result, &ir.Argument{
			Name: value.GetName(), Type: MapType(value.GetArgType()),
			Mode: mode, DefaultValue: m.expr(value.GetDefexpr()),
		})
	}

	return result
}

func returnType(value *pg.CreateFunctionStmt) *ir.ReturnType {
	table := &ir.TableReturn{}

	for _, node := range value.GetParameters() {
		parameter := node.GetFunctionParameter()
		if parameter.GetMode() == pg.FunctionParameterMode_FUNC_PARAM_TABLE {
			table.Columns = append(table.Columns, &ir.TableColumn{
				Name: parameter.GetName(), Type: MapType(parameter.GetArgType()),
			})
		}
	}

	if len(table.GetColumns()) > 0 {
		return &ir.ReturnType{Kind: &ir.ReturnType_Table{Table: table}}
	}

	typ := value.GetReturnType()
	if typ == nil {
		return outputReturn(value.GetParameters())
	}

	mapped := MapType(typ)
	if typ.GetSetof() {
		return &ir.ReturnType{Kind: &ir.ReturnType_Setof{Setof: &ir.SetofType{Type: mapped}}}
	}

	switch mapped.GetPgName() {
	case "void":
		return &ir.ReturnType{Kind: &ir.ReturnType_VoidValue{VoidValue: true}}
	case "trigger":
		return &ir.ReturnType{Kind: &ir.ReturnType_TriggerValue{TriggerValue: true}}
	default:
		return &ir.ReturnType{Kind: &ir.ReturnType_Scalar{Scalar: mapped}}
	}
}

func outputReturn(nodes []*pg.Node) *ir.ReturnType {
	columns := make([]*ir.TableColumn, 0, len(nodes))

	for _, node := range nodes {
		parameter := node.GetFunctionParameter()
		if parameter.GetMode() == pg.FunctionParameterMode_FUNC_PARAM_OUT ||
			parameter.GetMode() == pg.FunctionParameterMode_FUNC_PARAM_INOUT {
			columns = append(columns, &ir.TableColumn{Name: parameter.GetName(), Type: MapType(parameter.GetArgType())})
		}
	}

	switch len(columns) {
	case 0:
		return nil
	case 1:
		return &ir.ReturnType{Kind: &ir.ReturnType_Scalar{Scalar: columns[0].GetType()}}
	default:
		return &ir.ReturnType{Kind: &ir.ReturnType_Table{Table: &ir.TableReturn{Columns: columns}}}
	}
}

func (m Mapper) functionOptions(result *ir.Function, nodes []*pg.Node) {
	for _, node := range nodes {
		option := node.GetDefElem()
		arg := option.GetArg()

		switch option.GetDefname() {
		case "language":
			result.Language = arg.GetString_().GetSval()
		case "as":
			result.Body = strings.Join(stringsOf(arg.GetList().GetItems()), "\n")
		case "strict":
			result.NullInput = ir.NullInputBehavior_NULL_INPUT_BEHAVIOR_CALLED
			if booleanValue(arg) {
				result.NullInput = ir.NullInputBehavior_NULL_INPUT_BEHAVIOR_STRICT
			}
		case "security":
			result.SecurityDefiner = booleanValue(arg)
		case "leakproof":
			result.Leakproof = booleanValue(arg)
		case "cost":
			result.EstimatedCost = estimate(arg)
		case "rows":
			result.EstimatedRows = estimate(arg)
		case "volatility":
			result.Volatility = volatility(arg.GetString_().GetSval())
		case "parallel":
			result.Parallel = parallelSafety(arg.GetString_().GetSval())
		}
	}
}

func estimate(node *pg.Node) int32 {
	value := integerValue(node)

	const maxEstimate = 1<<31 - 1

	if value < 0 || value > maxEstimate {
		return 0
	}

	return int32(value)
}

func volatility(value string) ir.Volatility {
	switch value {
	case "immutable":
		return ir.Volatility_VOLATILITY_IMMUTABLE
	case "stable":
		return ir.Volatility_VOLATILITY_STABLE
	default:
		return ir.Volatility_VOLATILITY_VOLATILE
	}
}

func parallelSafety(value string) ir.ParallelSafety {
	switch value {
	case "safe":
		return ir.ParallelSafety_PARALLEL_SAFETY_SAFE
	case "restricted":
		return ir.ParallelSafety_PARALLEL_SAFETY_RESTRICTED
	default:
		return ir.ParallelSafety_PARALLEL_SAFETY_UNSAFE
	}
}

func (m Mapper) sqlBody(node *pg.Node) []*ir.SelectStmt {
	if node == nil {
		return nil
	}

	if node.GetSelectStmt() != nil {
		return []*ir.SelectStmt{m.selectStmt(node.GetSelectStmt())}
	}

	if value := node.GetReturnStmt(); value != nil {
		target := &pg.Node{Node: &pg.Node_ResTarget{ResTarget: &pg.ResTarget{Val: value.GetReturnval()}}}

		return []*ir.SelectStmt{m.selectStmt(&pg.SelectStmt{TargetList: []*pg.Node{target}})}
	}

	var result []*ir.SelectStmt
	for _, child := range node.GetList().GetItems() {
		result = append(result, m.sqlBody(child)...)
	}

	return result
}

// MapCreateTrigger decodes PostgreSQL's trigger event bitmask in IR enum order.
func (m Mapper) MapCreateTrigger(value *pg.CreateTrigStmt) *ir.Trigger {
	if value == nil {
		return nil
	}

	const (
		before    = 2
		insteadOf = 64
	)

	result := &ir.Trigger{
		Name: value.GetTrigname(), Table: tableObject(value.GetRelation()),
		Constraint: value.GetIsconstraint(), Timing: ir.TriggerTiming_TRIGGER_TIMING_AFTER,
		Level: ir.TriggerLevel_TRIGGER_LEVEL_STATEMENT, UpdateColumns: stringsOf(value.GetColumns()),
		When: m.expr(value.GetWhenClause()), Arguments: stringsOf(value.GetArgs()),
		Function: &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_FUNCTION, Name: qualified(value.GetFuncname())},
	}
	result.GetFunction().GetName().Parts = append(result.GetFunction().GetName().GetParts(), "")

	if value.GetTiming()&before != 0 {
		result.Timing = ir.TriggerTiming_TRIGGER_TIMING_BEFORE
	}

	if value.GetTiming()&insteadOf != 0 {
		result.Timing = ir.TriggerTiming_TRIGGER_TIMING_INSTEAD_OF
	}

	if value.GetRow() {
		result.Level = ir.TriggerLevel_TRIGGER_LEVEL_ROW
	}

	for _, event := range []struct {
		bit  int32
		kind ir.TriggerEvent
	}{
		{4, ir.TriggerEvent_TRIGGER_EVENT_INSERT},
		{16, ir.TriggerEvent_TRIGGER_EVENT_UPDATE},
		{8, ir.TriggerEvent_TRIGGER_EVENT_DELETE},
		{32, ir.TriggerEvent_TRIGGER_EVENT_TRUNCATE},
	} {
		if value.GetEvents()&event.bit != 0 {
			result.Events = append(result.Events, event.kind)
		}
	}

	m.finish(result)

	return result
}

// MapMaterializedView maps only CREATE MATERIALIZED VIEW, not CREATE TABLE AS.
func (m Mapper) MapMaterializedView(value *pg.CreateTableAsStmt) *ir.MaterializedView {
	if value == nil || value.GetObjtype() != pg.ObjectType_OBJECT_MATVIEW {
		return nil
	}

	into := value.GetInto()
	result := &ir.MaterializedView{
		Name: relationName(into.GetRel()), Columns: stringsOf(into.GetColNames()),
		WithData: !into.GetSkipData(), Tablespace: into.GetTableSpaceName(),
		Query: m.selectStmt(value.GetQuery().GetSelectStmt()),
	}
	m.finish(result)

	return result
}
