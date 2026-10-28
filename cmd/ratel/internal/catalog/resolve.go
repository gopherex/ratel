package catalog

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
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
			return cmp.Compare(ObjectID(&ir.ObjectRef{Name: left.GetName()}), ObjectID(&ir.ObjectRef{Name: right.GetName()}))
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

	return refusal("RTL-C03", metadata(pending.message), "undeclared table "+ObjectID(reference))
}

func (b *builder) attachTable(pending attachment) error {
	reference := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: pending.table}

	entry, exists := b.objects[objectKey(reference)]
	if !exists {
		return refusal("RTL-C03", metadata(pending.message), "undeclared table "+ObjectID(reference))
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
		return chooseName(relation, "", "pkey", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_FOREIGN_KEY:
		return chooseName(relation, nameAddition(constraintColumns(constraint)), "fkey", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_UNIQUE:
		return chooseName(relation, nameAddition(constraintColumns(constraint)), "key", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_NOT_NULL:
		return chooseName(relation, constraint.GetNotNullColumn(), "not_null", used)
	case ir.ConstraintType_CONSTRAINT_TYPE_EXCLUSION:
		return chooseName(relation, nameAddition(exclusionColumns(constraint.GetExclusion())), "excl", used)
	default:
		// A check names its column only when it references exactly one.
		columns := referencedColumns(constraint.GetCheck().GetExpression())
		if len(columns) == 1 {
			return chooseName(relation, columns[0], "check", used)
		}

		return chooseName(relation, "", "check", used)
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

	return refusal("RTL-C03", meta, "undeclared column "+ObjectID(&ir.ObjectRef{Name: table.GetName()})+"."+name)
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
		if entry, exists := b.namespaces["type/"+ObjectID(reference)]; exists {
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

	return refusal("RTL-C03", meta, "undeclared reference "+ObjectID(reference))
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
	key := "type/" + ObjectID(&ir.ObjectRef{Name: name})

	entry, exists := b.namespaces[key]
	if !exists {
		return refusal("RTL-C03", meta, "undeclared type "+ObjectID(&ir.ObjectRef{Name: name}))
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

// resolveCall binds a call to one routine. Inside a query body, column types
// come from the query's own scope, which query inference resolves; there an
// overload left ambiguous by untyped arguments keeps no target.
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

	candidates := b.bestCandidates(call, scope)
	if query && len(candidates) > 1 {
		return nil
	}

	if len(candidates) != 1 {
		return refusal("RTL-C03", meta, "undeclared or ambiguous function "+strings.Join(parts, "."))
	}

	call.Target = &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_FUNCTION,
		Name: &ir.QualifiedName{Parts: slices.Clone(candidates[0].GetName().GetParts())},
	}

	return nil
}

// bestCandidates returns the routines that take the call with the highest
// match score.
func (b *builder) bestCandidates(call *ir.FunctionCall, scope string) []*ir.Function {
	candidates, best := make([]*ir.Function, 0, 1), 0

	consider := func(function *ir.Function, scope string) {
		score, ok := b.matchCall(function, call, scope)

		switch {
		case !ok || score < best:
		case score > best || len(candidates) == 0:
			candidates, best = append(candidates[:0], function), score
		default:
			candidates = append(candidates, function)
		}
	}

	for _, entry := range b.objects {
		switch function := entry.message.(type) {
		case *ir.Function:
			consider(function, scope)
		default:
		}
	}

	for _, schema := range b.builtins {
		for _, function := range schema.GetFunctions() {
			consider(function, pgCatalog)
		}
	}

	return candidates
}

// Argument match scores, after PostgreSQL's func_select_candidate: an exact
// type beats a domain over it, which beats an implicit cast.
const (
	coercedArgument = iota
	domainArgument
	exactArgument
)

// matchCall scores a candidate for the call; ok is false when its arity or an
// argument type rules it out.
func (b *builder) matchCall(function *ir.Function, call *ir.FunctionCall, scope string) (int, bool) {
	parts := call.GetName().GetParts()

	prefix := slices.Clone(parts)
	if len(prefix) == 1 {
		prefix = append([]string{scope}, prefix...)
	}

	candidate := function.GetName().GetParts()
	if len(candidate) == 0 || !slices.Equal(candidate[:len(candidate)-1], prefix) {
		return 0, false
	}

	inputs, fits := callInputs(function, len(call.GetArguments()))
	if !fits {
		return 0, false
	}

	score := 0

	for index, expected := range inputs {
		points, ok := b.matchArgument(expected, call.GetArguments()[index])
		if !ok {
			return 0, false
		}

		score += points
	}

	return score, true
}

// callInputs lists the input types a call with count arguments binds to,
// filling trailing defaults and expanding a variadic element; fits is false
// when the arity does not fit.
func callInputs(function *ir.Function, count int) ([]*ir.TypeRef, bool) {
	var inputs []*ir.TypeRef

	for _, argument := range function.GetArguments() {
		if argument.GetMode() != ir.ArgMode_ARG_MODE_OUT {
			inputs = append(inputs, argument.GetType())
		}
	}

	minimum := len(inputs) - int(function.GetDefaultArguments())

	if element := function.GetVariadicElement(); element != nil {
		inputs = inputs[:len(inputs)-1]
		minimum = min(minimum, len(inputs))

		for len(inputs) < count {
			inputs = append(inputs, element)
		}
	}

	if count < minimum || count > len(inputs) {
		return nil, false
	}

	return inputs[:count], true
}

func (b *builder) matchArgument(expected *ir.TypeRef, actual *ir.Expr) (int, bool) {
	given := b.argumentType(actual)

	switch {
	case given == nil:
		return coercedArgument, true
	case strings.HasPrefix(expected.GetPgName(), "any"):
		return coercedArgument, acceptsPolymorphic(expected.GetPgName(), b.baseType(given))
	case sameType(given, expected):
		return exactArgument, true
	default:
	}

	base := b.baseType(given)

	switch {
	case base == nil:
		return 0, false
	case sameType(base, expected):
		return domainArgument, true
	case base.GetUdt() == nil && expected.GetUdt() == nil && b.casts[base.GetPgName()+":"+expected.GetPgName()]:
		return coercedArgument, true
	default:
		return 0, false
	}
}

// acceptsPolymorphic reports whether a polymorphic parameter takes the type,
// by the family the pseudo-type names.
func acceptsPolymorphic(parameter string, given *ir.TypeRef) bool {
	switch parameter {
	case "anyarray", "anycompatiblearray":
		return given.GetElement() != nil
	case "anynonarray", "anycompatiblenonarray":
		return given.GetElement() == nil
	case "anyenum":
		return given.GetKind() == ir.TypeKind_TYPE_KIND_ENUM
	case "anyrange", "anycompatiblerange":
		return given.GetKind() == ir.TypeKind_TYPE_KIND_RANGE || strings.HasSuffix(given.GetPgName(), "range")
	case "anymultirange", "anycompatiblemultirange":
		return strings.HasSuffix(given.GetPgName(), "multirange")
	default:
		return given != nil
	}
}

func sameType(left, right *ir.TypeRef) bool {
	if left.GetUdt() != nil || right.GetUdt() != nil {
		return left.GetUdt() != nil && right.GetUdt() != nil && objectKey(left.GetUdt()) == objectKey(right.GetUdt())
	}

	return left.GetPgName() == right.GetPgName()
}

// argumentType is the type of an argument before coercion; nil is unknown:
// an untyped string literal, NULL, or an expression this resolver does not
// type, which PostgreSQL also coerces to the candidate.
func (b *builder) argumentType(actual *ir.Expr) *ir.TypeRef {
	if cast := actual.GetCast(); cast != nil {
		return b.resolvedType(cast.GetTargetType(), defaultSchema)
	}

	if target := actual.GetColumnRef().GetTarget(); target != nil {
		entry := b.objects[objectKey(target)]
		switch column := entry.message.(type) {
		case *ir.Column:
			return b.resolvedType(column.GetType(), entry.scope)
		default:
			return nil
		}
	}

	switch value := actual.GetLiteral().GetValue().(type) {
	case *ir.Literal_IntValue:
		if value.IntValue >= math.MinInt32 && value.IntValue <= math.MaxInt32 {
			return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: integer}
		}

		return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: bigInteger}
	case *ir.Literal_FloatValue, *ir.Literal_NumericValue:
		return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: "numeric"}
	case *ir.Literal_BoolValue:
		return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: "bool"}
	default:
		return nil
	}
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

// baseType unwraps domains to their base type; nil when one is undeclared.
func (b *builder) baseType(typ *ir.TypeRef) *ir.TypeRef {
	for range len(b.objects) + 1 {
		if typ.GetKind() != ir.TypeKind_TYPE_KIND_DOMAIN {
			return typ
		}

		entry := b.objects[objectKey(typ.GetUdt())]
		switch domain := entry.message.(type) {
		case *ir.DomainType:
			typ = b.resolvedType(domain.GetBaseType(), entry.scope)
		default:
			return nil
		}
	}

	return nil
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
