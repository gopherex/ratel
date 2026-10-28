package catalog

import (
	"cmp"
	"maps"
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
			value.Name = b.uniqueRelationName(schema, indexDefaultName(table, value))
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

func constraintName(table *ir.Table, constraint *ir.Constraint) string {
	parts := []string{table.GetName().GetParts()[1]}
	parts = append(parts, constraintColumns(constraint)...)

	suffix := strings.TrimPrefix(strings.ToLower(constraint.GetType().String()), "constraint_type_")

	switch constraint.GetType() {
	case ir.ConstraintType_CONSTRAINT_TYPE_PRIMARY_KEY:
		suffix = "pkey"
		parts = parts[:1]
	case ir.ConstraintType_CONSTRAINT_TYPE_FOREIGN_KEY:
		suffix = "fkey"
	case ir.ConstraintType_CONSTRAINT_TYPE_UNIQUE:
		suffix = "key"
	default:
	}

	return strings.Join(append(parts, suffix), "_")
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
	sql := "CREATE FUNCTION ratel_reference(" + signature + ") RETURNS int LANGUAGE sql AS 'SELECT 1'"

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
		if err := b.resolveCall(value, scope, meta); err != nil {
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
		if _, exists := b.types.TypeForName(value.GetPgName()); exists {
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

func (b *builder) resolveCall(call *ir.FunctionCall, scope string, meta *ir.Metadata) error {
	parts := call.GetName().GetParts()
	if len(parts) == 0 {
		return refusal("RTL-C03", meta, "undeclared function")
	}

	name := parts[len(parts)-1]
	if (len(parts) == 1 || parts[0] == pgCatalog) &&
		(name == "nextval" || name == "currval" || name == "setval") {
		return b.sequenceCall(call, scope, meta)
	}

	var candidates []*ir.Function

	for _, entry := range b.objects {
		switch function := entry.message.(type) {
		case *ir.Function:
			if matchesCall(function, call, scope) {
				candidates = append(candidates, function)
			}
		default:
		}
	}

	for _, schema := range b.builtins {
		for _, function := range schema.GetFunctions() {
			if matchesCall(function, call, pgCatalog) {
				candidates = append(candidates, function)
			}
		}
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

func matchesCall(function *ir.Function, call *ir.FunctionCall, scope string) bool {
	parts := call.GetName().GetParts()

	prefix := slices.Clone(parts)
	if len(prefix) == 1 {
		prefix = append([]string{scope}, prefix...)
	}

	candidate := function.GetName().GetParts()
	if len(candidate) == 0 || !slices.Equal(candidate[:len(candidate)-1], prefix) {
		return false
	}

	var arguments []*ir.Argument

	for _, argument := range function.GetArguments() {
		if argument.GetMode() != ir.ArgMode_ARG_MODE_OUT {
			arguments = append(arguments, argument)
		}
	}

	if len(arguments) != len(call.GetArguments()) {
		return false
	}

	for index, argument := range arguments {
		if !matchesArgument(argument.GetType(), call.GetArguments()[index]) {
			return false
		}
	}

	return true
}

func matchesArgument(expected *ir.TypeRef, actual *ir.Expr) bool {
	name := expected.GetPgName()
	if actual.GetCast() != nil {
		return name == actual.GetCast().GetTargetType().GetPgName()
	}

	switch actual.GetLiteral().GetValue().(type) {
	case *ir.Literal_IntValue:
		return name == smallInteger || name == integer || name == bigInteger
	case *ir.Literal_StringValue:
		return name == "text" || name == "varchar" || name == "bpchar"
	case *ir.Literal_BoolValue:
		return name == "bool"
	default:
		return true
	}
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
