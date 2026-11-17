package catalog

import (
	"cmp"
	"errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/objectid"
	"github.com/gopherex/ratel/cmd/ratel/internal/overload"
	"github.com/gopherex/ratel/cmd/ratel/internal/pgname"
)

func (b *builder) finish() error {
	slices.SortStableFunc(b.attachments, func(left, right attachment) int {
		return cmp.Compare(attachmentKey(left), attachmentKey(right))
	})

	for _, pending := range b.attachments {
		if err := b.attach(pending); err != nil {
			return err
		}
	}

	for _, name := range slices.Sorted(maps.Keys(b.schemas)) {
		schema := b.schemas[name]
		slices.SortFunc(schema.GetTables(), func(left, right *ir.Table) int {
			return cmp.Compare(
				objectid.ID(&ir.ObjectRef{Name: left.GetName()}), objectid.ID(&ir.ObjectRef{Name: right.GetName()}),
			)
		})

		for _, table := range schema.GetTables() {
			if err := b.tableObjects(table); err != nil {
				return err
			}
		}

		b.catalog.Schemas = append(b.catalog.Schemas, schema)
	}

	for _, entry := range b.objects {
		if entry.reference.GetKind() == ir.ObjectKind_OBJECT_KIND_SCHEMA ||
			entry.reference.GetKind() == ir.ObjectKind_OBJECT_KIND_EXTENSION {
			continue
		}

		scope := entry.scope
		if err := b.resolveTree(entry.message, scope, entry.meta, nil); err != nil {
			return err
		}
	}

	for _, schema := range b.catalog.GetSchemas() {
		for _, grant := range schema.GetGrants() {
			if err := b.resolveReference(grant.GetObject(), schema.GetName(), grant.GetMeta()); err != nil {
				return err
			}
		}
	}

	if err := b.applyComments(); err != nil {
		return err
	}

	normalize(b.catalog.ProtoReflect(), "catalog")

	return nil
}

func (b *builder) applyComments() error {
	seenComments := make(map[string]*ir.Metadata)

	for _, pending := range b.comments {
		key := objectKey(pending.reference)
		if previous, exists := seenComments[key]; exists {
			return refusal("RTL-C02", pending.meta, "duplicate comment; first declared at "+position(previous.GetSource()))
		}

		seenComments[key] = pending.meta

		if err := b.resolveReference(pending.reference, defaultSchema, pending.meta); err != nil {
			return err
		}

		entry := b.objects[objectKey(pending.reference)]
		entry.meta.Comment = pending.meta.GetComment()
	}

	return nil
}

func (b *builder) attach(pending attachment) error {
	reference := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: pending.table}
	if _, exists := b.objects[objectKey(reference)]; exists {
		return b.attachTable(pending)
	}

	view := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW, Name: pending.table}
	if materialized, found := b.objects[objectKey(view)]; found {
		return b.attachViewIndex(materialized, pending)
	}

	return refusal("RTL-C03", metadata(pending.message), "undeclared table "+objectid.ID(reference))
}

func (b *builder) attachTable(pending attachment) error {
	reference := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: pending.table}

	entry, exists := b.objects[objectKey(reference)]
	if !exists {
		return refusal("RTL-C03", metadata(pending.message), "undeclared table "+objectid.ID(reference))
	}

	var table *ir.Table
	switch value := entry.message.(type) {
	case *ir.Table:
		table = value
	default:
		return refusal("RTL-C03", entry.meta, "target is not a table")
	}

	switch value := pending.message.(type) {
	case *ir.Table:
		table.Constraints = append(table.GetConstraints(), value.GetConstraints()...)
		table.RlsEnabled = table.GetRlsEnabled() || value.GetRlsEnabled()
		table.RlsForced = table.GetRlsForced() || value.GetRlsForced()
	case *ir.Index:
		if value.GetName() == "" {
			schema := table.GetName().GetParts()[0]
			value.Name = b.uniqueRelationName(schema, pending.table.GetParts()[1], indexColumns(value), "idx")
		}

		ref := &ir.ObjectRef{
			Kind: ir.ObjectKind_OBJECT_KIND_INDEX,
			Name: &ir.QualifiedName{Parts: []string{pending.table.GetParts()[0], value.GetName()}},
		}
		if err := b.register(ref, value, value.GetMeta(), "relation"); err != nil {
			return err
		}

		table.Indexes = append(table.GetIndexes(), value)
	case *ir.Policy:
		if err := b.registerChild(reference, value.GetName(), ir.ObjectKind_OBJECT_KIND_POLICY, value); err != nil {
			return err
		}

		table.Policies = append(table.GetPolicies(), value)
	case *ir.Trigger:
		if err := b.registerChild(reference, value.GetName(), ir.ObjectKind_OBJECT_KIND_TRIGGER, value); err != nil {
			return err
		}

		schema := b.schemas[pending.table.GetParts()[0]]
		schema.Triggers = append(schema.GetTriggers(), value)
		table.Triggers = append(table.GetTriggers(), &ir.ObjectRef{
			Kind: ir.ObjectKind_OBJECT_KIND_TRIGGER,
			Name: &ir.QualifiedName{Parts: append(slices.Clone(pending.table.GetParts()), value.GetName())},
		})
	}

	return nil
}

// attachViewIndex attaches an index to a materialized view, the only
// non-table relation that takes one.
func (b *builder) attachViewIndex(entry declaration, pending attachment) error {
	var (
		view  *ir.MaterializedView
		index *ir.Index
	)

	switch value := entry.message.(type) {
	case *ir.MaterializedView:
		view = value
	default:
		return refusal("RTL-C03", entry.meta, "target is not a materialized view")
	}

	switch value := pending.message.(type) {
	case *ir.Index:
		index = value
	default:
		return refusal("RTL-C03", metadata(pending.message), "target is not a table")
	}

	schema := pending.table.GetParts()[0]
	if index.GetName() == "" {
		index.Name = b.uniqueRelationName(schema, pending.table.GetParts()[1], indexColumns(index), "idx")
	}

	ref := &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_INDEX, Name: &ir.QualifiedName{Parts: []string{schema, index.GetName()}},
	}
	if err := b.register(ref, index, index.GetMeta(), "relation"); err != nil {
		return err
	}

	view.Indexes = append(view.GetIndexes(), index)

	return nil
}

func (b *builder) registerChild(parent *ir.ObjectRef, name string, kind ir.ObjectKind, message proto.Message) error {
	reference := &ir.ObjectRef{
		Kind: kind, Name: &ir.QualifiedName{Parts: append(slices.Clone(parent.GetName().GetParts()), name)},
	}

	if err := b.register(reference, message, metadata(message), namespace(kind)); err != nil {
		return err
	}

	entry := b.objects[objectKey(reference)]
	entry.scope = b.objects[objectKey(parent)].scope
	b.objects[objectKey(reference)] = entry

	return nil
}

func (b *builder) tableObjects(table *ir.Table) error {
	reference := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: table.GetName()}

	if table.GetPartitionOf() != nil {
		parent := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: table.GetPartitionOf()}
		if err := b.resolveReference(parent, b.objects[objectKey(reference)].scope, table.GetMeta()); err != nil {
			return err
		}
	}

	for _, column := range table.GetColumns() {
		if err := b.registerChild(reference, column.GetName(), ir.ObjectKind_OBJECT_KIND_COLUMN, column); err != nil {
			return err
		}
	}

	if err := b.expandColumns(table); err != nil {
		return err
	}

	ensureNotNull(table)
	nameConstraints(table)

	for _, constraint := range table.GetConstraints() {
		kind := ir.ObjectKind_OBJECT_KIND_CONSTRAINT
		if err := b.registerChild(reference, constraint.GetName(), kind, constraint); err != nil {
			return err
		}

		for _, name := range constraintColumns(constraint) {
			if err := b.columnExists(table, name, constraint.GetMeta()); err != nil {
				return err
			}
		}
	}

	if err := b.validateLocalColumns(table, table, table.GetMeta()); err != nil {
		return err
	}

	return b.validateIndexes(table)
}

func (b *builder) validateLocalColumns(message proto.Message, table *ir.Table, meta *ir.Metadata) error {
	if local := metadata(message); local != nil {
		meta = local
	}

	switch value := message.(type) {
	case *ir.SelectStmt:
		return nil
	case *ir.ColumnRef:
		if err := b.columnExists(table, value.GetColumn(), meta); err != nil {
			return err
		}

		value.Target = &ir.ObjectRef{
			Kind: ir.ObjectKind_OBJECT_KIND_COLUMN,
			Name: &ir.QualifiedName{Parts: append(slices.Clone(table.GetName().GetParts()), value.GetColumn())},
		}
	default:
	}

	return children(message.ProtoReflect(), func(child protoreflect.Message, _ string) error {
		return b.validateLocalColumns(child.Interface(), table, meta)
	})
}

func (b *builder) validateIndexes(table *ir.Table) error {
	for _, index := range table.GetIndexes() {
		names := slices.Clone(index.GetInclude())

		for _, element := range index.GetElements() {
			if element.GetColumn() != "" {
				names = append(names, element.GetColumn())
			}
		}

		for _, name := range names {
			if err := b.columnExists(table, name, index.GetMeta()); err != nil {
				return err
			}
		}
	}

	return nil
}

func constraintColumns(constraint *ir.Constraint) []string {
	switch {
	case constraint.GetPrimaryKey() != nil:
		return constraint.GetPrimaryKey().GetColumns()
	case constraint.GetUnique() != nil:
		return constraint.GetUnique().GetColumns()
	case constraint.GetForeignKey() != nil:
		return constraint.GetForeignKey().GetColumns()
	case constraint.GetNotNullColumn() != "":
		return []string{constraint.GetNotNullColumn()}
	default:
		return nil
	}
}

// constraintName chooses the name PostgreSQL gives an unnamed constraint from
// the table, a column addition and the label.
func constraintName(table *ir.Table, constraint *ir.Constraint, used func(string) bool) string {
	relation := table.GetName().GetParts()[1]

	switch constraint.GetType() {
	case ir.ConstraintType_CONSTRAINT_TYPE_PRIMARY_KEY:
		return pgname.Choose(relation, "", "pkey", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_FOREIGN_KEY:
		return pgname.Choose(relation, pgname.Addition(constraintColumns(constraint)), "fkey", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_UNIQUE:
		return pgname.Choose(relation, pgname.Addition(constraintColumns(constraint)), "key", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_NOT_NULL:
		return pgname.Choose(relation, constraint.GetNotNullColumn(), "not_null", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_EXCLUSION:
		return pgname.Choose(relation, pgname.Addition(exclusionColumns(constraint.GetExclusion())), "excl", used)
	default:
		// A check names its column only when it references exactly one.
		columns := referencedColumns(constraint.GetCheck().GetExpression())
		if len(columns) == 1 {
			return pgname.Choose(relation, columns[0], "check", used)
		}

		return pgname.Choose(relation, "", "check", used)
	}
}

func exclusionColumns(exclusion *ir.ExclusionConstraint) []string {
	columns := make([]string, 0, len(exclusion.GetElements()))
	for _, element := range exclusion.GetElements() {
		columns = append(columns, cmp.Or(element.GetColumn(), expressionColumnName(element.GetExpr())))
	}

	return columns
}

// expressionColumnName is FigureIndexColname: a function call is named after
// the function, any other expression "expr".
func expressionColumnName(expression *ir.Expr) string {
	function := expression.GetFunctionCall().GetName().GetParts()
	if len(function) > 0 {
		return function[len(function)-1]
	}

	return "expr"
}

// referencedColumns lists the distinct columns an expression references, in
// first-reference order.
func referencedColumns(expression *ir.Expr) []string {
	var columns []string

	var walk func(protoreflect.Message)
	walk = func(message protoreflect.Message) {
		switch reference := message.Interface().(type) {
		case *ir.ColumnRef:
			if !slices.Contains(columns, reference.GetColumn()) {
				columns = append(columns, reference.GetColumn())
			}
		default:
		}

		_ = children(message, func(child protoreflect.Message, _ string) error {
			walk(child)

			return nil
		})
	}

	if expression != nil {
		walk(expression.ProtoReflect())
	}

	return columns
}

func (b *builder) columnExists(table *ir.Table, name string, meta *ir.Metadata) error {
	for _, column := range table.GetColumns() {
		if column.GetName() == name {
			return nil
		}
	}

	return refusal("RTL-C03", meta, "undeclared column "+objectid.ID(&ir.ObjectRef{Name: table.GetName()})+"."+name)
}

func (b *builder) resolveReference(reference *ir.ObjectRef, scope string, meta *ir.Metadata) error {
	qualify(reference.GetName(), scope, reference.GetKind())

	if _, exists := b.objects[objectKey(reference)]; exists {
		return nil
	}

	if reference.GetKind() == ir.ObjectKind_OBJECT_KIND_FUNCTION ||
		reference.GetKind() == ir.ObjectKind_OBJECT_KIND_PROCEDURE {
		if err := b.resolveRoutineReference(reference, scope, meta); err != nil {
			return err
		}

		if _, exists := b.objects[objectKey(reference)]; exists {
			return nil
		}
	}

	if reference.GetKind() == ir.ObjectKind_OBJECT_KIND_UNSPECIFIED {
		if entry, exists := b.namespaces["type/"+objectid.ID(reference)]; exists {
			reference.Kind = entry.reference.GetKind()

			return nil
		}
	}
	// TABLE in the parser also represents a view or a materialized view.
	if reference.GetKind() == ir.ObjectKind_OBJECT_KIND_TABLE {
		for _, kind := range []ir.ObjectKind{ir.ObjectKind_OBJECT_KIND_VIEW, ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW} {
			candidate := &ir.ObjectRef{Kind: kind, Name: reference.GetName()}
			if _, exists := b.objects[objectKey(candidate)]; exists {
				reference.Kind = kind

				return nil
			}
		}
	}

	if b.builtinReference(reference) {
		return nil
	}

	return refusal("RTL-C03", meta, "undeclared reference "+objectid.ID(reference))
}

func (b *builder) resolveRoutineReference(reference *ir.ObjectRef, scope string, meta *ir.Metadata) error {
	parts := reference.GetName().GetParts()
	signature := parts[len(parts)-1]

	// "any" is a reserved word, so the pseudo-type is written quoted.
	types := strings.Split(signature, ",")
	for index, name := range types {
		if strings.TrimSpace(name) == "any" {
			types[index] = `"any"`
		}
	}

	sql := "CREATE FUNCTION ratel_reference(" + strings.Join(types, ",") + ") RETURNS int LANGUAGE sql AS 'SELECT 1'"

	parsed, err := b.parser.Parse(sql)
	if err != nil || len(parsed.GetStmts()) != 1 {
		return refusal("RTL-C03", meta, "invalid routine signature "+signature)
	}

	syntax := parsed.GetStmts()[0].GetStmt().GetCreateFunctionStmt()
	arguments := make([]*ir.Argument, 0, len(syntax.GetParameters()))

	for _, parameter := range syntax.GetParameters() {
		arguments = append(arguments, &ir.Argument{Type: mapper.MapType(parameter.GetFunctionParameter().GetArgType())})
	}

	return b.routineName(reference.GetName(), arguments, scope, meta)
}

func (b *builder) builtinReference(reference *ir.ObjectRef) bool {
	for _, schema := range b.builtins {
		for _, function := range schema.GetFunctions() {
			candidate := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_FUNCTION, Name: function.GetName()}
			if objectKey(reference) == objectKey(candidate) {
				return true
			}
		}
	}

	return false
}

func (b *builder) resolveTree(message proto.Message, scope string, meta *ir.Metadata, ctes map[string]bool) error {
	if local := metadata(message); local != nil {
		meta = local
	}

	switch value := message.(type) {
	case *ir.TypeRef:
		if err := b.resolveType(value, scope, meta); err != nil {
			return err
		}
	case *ir.ObjectRef:
		return b.resolveReference(value, scope, meta)
	case *ir.ForeignKey:
		if err := b.resolveForeignKey(value, scope, meta); err != nil {
			return err
		}
	case *ir.FunctionCall:
		if err := b.resolveCall(value, scope, meta, ctes != nil); err != nil {
			return err
		}
	case *ir.SelectStmt:
		ctes = cteScope(ctes, value.GetWith())
	case *ir.TableRef:
		if len(value.GetName().GetParts()) == 1 && ctes[value.GetName().GetParts()[0]] {
			return nil
		}

		if value.GetTable() == nil {
			value.Table = &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: value.GetName()}
		}
	}

	return children(message.ProtoReflect(), func(child protoreflect.Message, _ string) error {
		return b.resolveTree(child.Interface(), scope, meta, ctes)
	})
}

func cteScope(previous map[string]bool, entries []*ir.CommonTableExpr) map[string]bool {
	scope := make(map[string]bool, len(previous)+len(entries))
	maps.Copy(scope, previous)

	for _, entry := range entries {
		scope[entry.GetName()] = true
	}

	return scope
}

func (b *builder) resolveType(value *ir.TypeRef, scope string, meta *ir.Metadata) error {
	if value.GetKind() == ir.TypeKind_TYPE_KIND_ARRAY || value.GetPgName() == "" {
		return nil
	}

	if value.GetUdt() == nil {
		// pgx knows a subset of builtins; the snapshot of the target major has
		// the rest (regclass, oid aliases, extension-free system types).
		if _, known := b.types.TypeForName(value.GetPgName()); known || b.snapshot[value.GetPgName()] {
			return nil
		}

		value.Udt = &ir.ObjectRef{Name: &ir.QualifiedName{Parts: []string{value.GetPgName()}}}
	}

	name := value.GetUdt().GetName()
	qualify(name, scope, ir.ObjectKind_OBJECT_KIND_ENUM_TYPE)
	key := "type/" + objectid.ID(&ir.ObjectRef{Name: name})

	entry, exists := b.namespaces[key]
	if !exists {
		return refusal("RTL-C03", meta, "undeclared type "+objectid.ID(&ir.ObjectRef{Name: name}))
	}

	value.Kind = resolvedTypeKind(entry.reference.GetKind())
	value.Udt = entry.reference

	return nil
}

func resolvedTypeKind(kind ir.ObjectKind) ir.TypeKind {
	switch kind {
	case ir.ObjectKind_OBJECT_KIND_ENUM_TYPE:
		return ir.TypeKind_TYPE_KIND_ENUM
	case ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE:
		return ir.TypeKind_TYPE_KIND_DOMAIN
	case ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, ir.ObjectKind_OBJECT_KIND_TABLE,
		ir.ObjectKind_OBJECT_KIND_VIEW, ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW:
		return ir.TypeKind_TYPE_KIND_COMPOSITE
	case ir.ObjectKind_OBJECT_KIND_RANGE_TYPE:
		return ir.TypeKind_TYPE_KIND_RANGE
	default:
		return ir.TypeKind_TYPE_KIND_USER_DEFINED
	}
}

func (b *builder) resolveForeignKey(value *ir.ForeignKey, scope string, meta *ir.Metadata) error {
	if err := b.resolveReference(value.GetReferencedTable(), scope, meta); err != nil {
		return err
	}

	entry := b.objects[objectKey(value.GetReferencedTable())]

	var table *ir.Table

	switch target := entry.message.(type) {
	case *ir.Table:
		table = target
	default:
		return refusal("RTL-C03", meta, "foreign key target is not a table")
	}

	if len(value.GetReferencedColumns()) == 0 {
		for _, constraint := range table.GetConstraints() {
			if constraint.GetPrimaryKey() != nil {
				value.ReferencedColumns = slices.Clone(constraint.GetPrimaryKey().GetColumns())

				break
			}
		}
	}

	if len(value.GetColumns()) != len(value.GetReferencedColumns()) {
		return refusal("RTL-C03", meta, "foreign key column count differs from referenced key")
	}

	for _, name := range value.GetReferencedColumns() {
		if err := b.columnExists(table, name, meta); err != nil {
			return err
		}
	}

	return nil
}

// resolveCall binds a call to one routine, choosing among overloads as
// PostgreSQL does (the overload package query inference shares). Inside a
// query body, column types come from the query's own scope, which query
// inference resolves: a call with an argument typed only there keeps no
// target, and so does one left ambiguous.
func (b *builder) resolveCall(call *ir.FunctionCall, scope string, meta *ir.Metadata, query bool) error {
	parts := call.GetName().GetParts()
	if len(parts) == 0 {
		return refusal("RTL-C03", meta, "undeclared function")
	}

	name := parts[len(parts)-1]
	if (len(parts) == 1 || parts[0] == pgCatalog) &&
		(name == "nextval" || name == "currval" || name == "setval") {
		return b.sequenceCall(call, scope, meta)
	}

	args, typed := b.argumentTypes(call.GetArguments(), scope)
	if !typed && query {
		return nil
	}

	chosen, err := b.overloads().Function(parts, b.routinePath(scope), args, nil, call.GetVariadic())
	if err != nil {
		if query {
			return nil
		}

		return refusal("RTL-C03", meta, callProblem(err))
	}

	call.Target = &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_FUNCTION,
		Name: &ir.QualifiedName{Parts: slices.Clone(chosen.Function.GetName().GetParts())},
	}

	return nil
}

// callProblem words a refused call: an undeclared one, or one several
// overloads take equally well, with its argument types.
func callProblem(err error) string {
	var refused *overload.Error
	if errors.As(err, &refused) && !refused.Ambiguous {
		return "undeclared " + refused.Call
	}

	return err.Error()
}

// routinePath is the schemas an unqualified routine name looks in after
// pg_catalog: the object's own schema, then the search path.
func (b *builder) routinePath(scope string) []string {
	result := []string{scope}
	for _, schema := range b.catalog.GetSearchPath() {
		if !slices.Contains(result, schema) {
			result = append(result, schema)
		}
	}

	return result
}

// overloads is the resolver over the builtins and the desired catalog,
// built once the catalog's schemas are assembled.
func (b *builder) overloads() *overload.Resolver {
	if b.resolver == nil {
		b.resolver = overload.New(b.catalog, b.builtins)
	}

	return b.resolver
}

// booleanSymbol reports the constructs that yield bool whatever their
// operands.
func booleanSymbol(symbol string) bool {
	switch symbol {
	case "AND", "OR", "NOT", "IS NULL", "IS NOT NULL", "IS TRUE", "IS NOT TRUE", "IS FALSE",
		"IS NOT FALSE", "IS UNKNOWN", "IS NOT UNKNOWN", "IN", "NOT IN", "LIKE", "NOT LIKE",
		"ILIKE", "NOT ILIKE", "SIMILAR TO", "NOT SIMILAR TO", "IS DISTINCT FROM",
		"IS NOT DISTINCT FROM", "BETWEEN", "NOT BETWEEN", "BETWEEN SYMMETRIC", "NOT BETWEEN SYMMETRIC":
		return true
	default:
		return strings.HasSuffix(symbol, " ANY") || strings.HasSuffix(symbol, " ALL")
	}
}

// argumentTypes types each expression; typed is false when one of them
// cannot be typed here (see exprType).
func (b *builder) argumentTypes(exprs []*ir.Expr, scope string) ([]*ir.TypeRef, bool) {
	result := make([]*ir.TypeRef, len(exprs))
	all := true

	for index, expr := range exprs {
		typ, typed := b.exprType(expr, scope)
		result[index] = typ
		all = all && typed
	}

	return result, all
}

// exprType is the type of an expression before coercion. typed is false
// when this resolver cannot type it (a column of a query's own scope, a
// subquery); a typed nil is an unknown: an untyped string literal or NULL,
// which PostgreSQL resolves from its context.
func (b *builder) exprType(actual *ir.Expr, scope string) (*ir.TypeRef, bool) {
	switch value := actual.GetNode().(type) {
	case *ir.Expr_Cast:
		typ := b.resolvedType(value.Cast.GetTargetType(), defaultSchema)

		return typ, typ != nil
	case *ir.Expr_ColumnRef:
		return b.columnType(value.ColumnRef)
	case *ir.Expr_Literal:
		return literalType(value.Literal)
	case *ir.Expr_Operator:
		return b.operatorType(value.Operator, scope)
	case *ir.Expr_FunctionCall:
		return b.callType(value.FunctionCall, scope)
	default:
		return nil, false
	}
}

func (b *builder) columnType(ref *ir.ColumnRef) (*ir.TypeRef, bool) {
	target := ref.GetTarget()
	if target == nil {
		return nil, false
	}

	entry := b.objects[objectKey(target)]
	switch column := entry.message.(type) {
	case *ir.Column:
		typ := b.resolvedType(column.GetType(), entry.scope)

		return typ, typ != nil
	default:
		return nil, false
	}
}

func literalType(literal *ir.Literal) (*ir.TypeRef, bool) {
	switch value := literal.GetValue().(type) {
	case *ir.Literal_IntValue:
		if value.IntValue >= math.MinInt32 && value.IntValue <= math.MaxInt32 {
			return overload.Scalar(integer), true
		}

		return overload.Scalar(bigInteger), true
	case *ir.Literal_FloatValue, *ir.Literal_NumericValue:
		return overload.Scalar("numeric"), true
	case *ir.Literal_BoolValue:
		return overload.Scalar("bool"), true
	case *ir.Literal_ByteaValue:
		return overload.Scalar("bytea"), true
	default:
		// A string literal and NULL are unknown until their context types them.
		return nil, true
	}
}

// operatorType resolves an operator expression as PostgreSQL does and
// yields its result type.
func (b *builder) operatorType(operator *ir.OperatorExpr, scope string) (*ir.TypeRef, bool) {
	symbol := operator.GetSymbol()
	if booleanSymbol(symbol) {
		return overload.Scalar("bool"), true
	}

	operands, typed := b.argumentTypes(operator.GetOperands(), scope)
	if !typed {
		return nil, false
	}

	if symbol == "NULLIF" {
		if len(operands) == 0 {
			return nil, false
		}

		return operands[0], true
	}

	// OPERATOR(pg_catalog.||) names the operator with its schema.
	if dot := strings.LastIndex(symbol, "."); dot >= 0 {
		symbol = symbol[dot+1:]
	}

	chosen, err := b.overloads().Operator(symbol, operands)
	if err != nil {
		return nil, false
	}

	return chosen.Result, true
}

// callType resolves a nested call and yields the type it returns.
func (b *builder) callType(call *ir.FunctionCall, scope string) (*ir.TypeRef, bool) {
	parts := call.GetName().GetParts()
	if len(parts) == 0 {
		return nil, false
	}

	args, typed := b.argumentTypes(call.GetArguments(), scope)
	if !typed {
		return nil, false
	}

	chosen, err := b.overloads().Function(parts, b.routinePath(scope), args, nil, call.GetVariadic())
	if err != nil {
		return nil, false
	}

	return chosen.Result, true
}

// resolvedType resolves a copy, so typing an argument never depends on
// whether the walk has reached the declaring object yet.
func (b *builder) resolvedType(typ *ir.TypeRef, scope string) *ir.TypeRef {
	clone := proto.CloneOf(typ)
	if b.resolveType(clone, scope, nil) != nil {
		return nil
	}

	return clone
}

func (b *builder) sequenceCall(call *ir.FunctionCall, scope string, meta *ir.Metadata) error {
	if len(call.GetArguments()) == 0 {
		return refusal("RTL-C03", meta, "sequence function requires an argument")
	}

	argument := call.GetArguments()[0]
	if argument.GetCast() != nil {
		argument = argument.GetCast().GetExpr()
	}

	name := argument.GetLiteral().GetStringValue()
	if name == "" {
		return refusal("RTL-C03", meta, "sequence reference must be a constant name")
	}

	parsed, err := b.parser.Parse("CREATE SEQUENCE " + name)
	if err != nil || len(parsed.GetStmts()) != 1 {
		return refusal("RTL-C03", meta, "invalid sequence reference "+name)
	}

	syntax := parsed.GetStmts()[0].GetStmt().GetCreateSeqStmt()
	if syntax == nil || len(syntax.GetOptions()) != 0 {
		return refusal("RTL-C03", meta, "invalid sequence reference "+name)
	}

	relation := syntax.GetSequence()
	parts := []string{relation.GetRelname()}

	if relation.GetSchemaname() != "" {
		parts = []string{relation.GetSchemaname(), relation.GetRelname()}
	}

	reference := &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_SEQUENCE, Name: &ir.QualifiedName{Parts: parts},
	}

	return b.resolveReference(reference, scope, meta)
}

func children(message protoreflect.Message, visit func(protoreflect.Message, string) error) error {
	fields := message.Descriptor().Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		if field.Message() == nil || !message.Has(field) {
			continue
		}

		if field.IsList() {
			list := message.Get(field).List()
			for item := range list.Len() {
				if err := visit(list.Get(item).Message(), string(field.Name())+"/"+strconv.Itoa(item)); err != nil {
					return err
				}
			}
		} else if err := visit(message.Get(field).Message(), string(field.Name())); err != nil {
			return err
		}
	}

	return nil
}
