package catalog

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"
)

func (b *builder) dispatch(stmt statement) error {
	node, mapped := stmt.syntax, stmt.mapper

	switch {
	case node.GetCreateStmt() != nil:
		table := mapped.MapCreateTable(node.GetCreateStmt())
		tableDefaults(table, node.GetCreateStmt())

		return b.add(table, "tables", stmt.schema)
	case node.GetCreateEnumStmt() != nil:
		return b.add(mapped.MapCreateEnum(node.GetCreateEnumStmt()), "enums", stmt.schema)
	case node.GetCreateDomainStmt() != nil:
		return b.add(mapped.MapCreateDomain(node.GetCreateDomainStmt()), "domains", stmt.schema)
	case node.GetCompositeTypeStmt() != nil:
		return b.add(mapped.MapCompositeType(node.GetCompositeTypeStmt()), "composites", stmt.schema)
	case node.GetCreateRangeStmt() != nil:
		return b.add(mapped.MapCreateRange(node.GetCreateRangeStmt()), "ranges", stmt.schema)
	case node.GetCreateSeqStmt() != nil:
		sequence := mapped.MapCreateSequence(node.GetCreateSeqStmt())
		sequenceDefaults(sequence, node.GetCreateSeqStmt().GetOptions())

		return b.add(sequence, "sequences", stmt.schema)
	case node.GetViewStmt() != nil:
		return b.add(mapped.MapView(node.GetViewStmt()), "views", stmt.schema)
	case node.GetCreateFunctionStmt() != nil:
		return b.addRoutine(stmt)
	case node.GetCreateTrigStmt() != nil:
		trigger := mapped.MapCreateTrigger(node.GetCreateTrigStmt())
		qualify(trigger.GetTable().GetName(), stmt.schema, ir.ObjectKind_OBJECT_KIND_TABLE)
		b.attachments = append(b.attachments, attachment{table: trigger.GetTable().GetName(), message: trigger})

		return nil
	case node.GetCreateExtensionStmt() != nil:
		return b.addExtension(mapped.MapCreateExtension(node.GetCreateExtensionStmt()))
	default:
		return b.dispatchAttachment(stmt)
	}
}

func (b *builder) addExtension(extension *ir.Extension) error {
	if extension.GetSchema() == "" {
		extension.Schema = defaultSchema
	}

	if b.schemas[extension.GetSchema()] == nil && extension.GetSchema() != pgCatalog {
		return refusal("RTL-C03", extension.GetMeta(), "undeclared schema "+extension.GetSchema())
	}

	reference := &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_EXTENSION, Name: &ir.QualifiedName{Parts: []string{extension.GetName()}},
	}
	if err := b.register(reference, extension, extension.GetMeta(), "extension"); err != nil {
		return err
	}

	b.catalog.Extensions = append(b.catalog.Extensions, extension)

	return nil
}

func (b *builder) addRoutine(stmt statement) error {
	function, procedure := stmt.mapper.MapCreateFunction(stmt.syntax.GetCreateFunctionStmt())
	if function != nil {
		if err := b.routineName(function.GetName(), function.GetArguments(), stmt.schema, function.GetMeta()); err != nil {
			return err
		}

		return b.add(function, "functions", stmt.schema)
	}

	if err := b.routineName(procedure.GetName(), procedure.GetArguments(), stmt.schema, procedure.GetMeta()); err != nil {
		return err
	}

	return b.add(procedure, "procedures", stmt.schema)
}

func (b *builder) routineName(name *ir.QualifiedName, arguments []*ir.Argument, scope string, meta *ir.Metadata) error {
	signature := make([]string, 0, len(arguments))

	for _, argument := range arguments {
		if err := b.resolveTree(argument.GetType(), scope, meta, nil); err != nil {
			return err
		}

		if argument.GetMode() != ir.ArgMode_ARG_MODE_OUT {
			signature = append(signature, typeSignature(argument.GetType()))
		}
	}

	name.Parts[len(name.GetParts())-1] = strings.Join(signature, ",")

	return nil
}

func (b *builder) dispatchAttachment(stmt statement) error {
	node, mapped := stmt.syntax, stmt.mapper

	var target *pg.RangeVar

	var message proto.Message

	switch {
	case node.GetCreateTableAsStmt() != nil:
		view := mapped.MapMaterializedView(node.GetCreateTableAsStmt())
		if view != nil {
			return b.add(view, "materialized_views", stmt.schema)
		}
	case node.GetIndexStmt() != nil:
		index := node.GetIndexStmt()
		if !index.GetConcurrent() {
			target = index.GetRelation()
			message = mapped.MapIndex(index)
		}
	case node.GetCreatePolicyStmt() != nil:
		policy := node.GetCreatePolicyStmt()
		target = policy.GetTable()
		message = mapped.MapCreatePolicy(policy)
	case node.GetAlterTableStmt() != nil:
		alter := node.GetAlterTableStmt()
		if fragment := mapped.MapAlterTable(alter); fragment != nil {
			target = alter.GetRelation()
			message = fragment
		}
	case node.GetCommentStmt() != nil:
		return b.addComment(stmt)
	case node.GetGrantStmt() != nil:
		grants := mapped.MapGrant(node.GetGrantStmt())
		if len(grants) > 0 {
			return b.grants(grants, stmt.schema, node.GetGrantStmt().GetObjtype())
		}
	default:
	}

	if message == nil {
		return disallowed(stmt)
	}

	name := &ir.QualifiedName{Parts: []string{target.GetRelname()}}
	if target.GetSchemaname() != "" {
		name.Parts = []string{target.GetSchemaname(), target.GetRelname()}
	}

	qualify(name, stmt.schema, ir.ObjectKind_OBJECT_KIND_TABLE)
	b.attachments = append(b.attachments, attachment{table: name, message: message})

	return nil
}

func (b *builder) addComment(stmt statement) error {
	syntax := stmt.syntax.GetCommentStmt()
	reference, meta := stmt.mapper.MapComment(syntax)
	kind := syntax.GetObjtype()

	if reference.GetKind() == ir.ObjectKind_OBJECT_KIND_UNSPECIFIED &&
		kind != pg.ObjectType_OBJECT_TYPE && kind != pg.ObjectType_OBJECT_DOMAIN {
		return disallowed(stmt)
	}

	qualify(reference.GetName(), stmt.schema, reference.GetKind())
	b.comments = append(b.comments, comment{reference: reference, meta: meta})

	return nil
}

func disallowed(stmt statement) error {
	meta := stmt.mapper.MapCreateSchema(&pg.CreateSchemaStmt{}).GetMeta()

	return refusal("RTL-C04", meta, "statement not allowed in declarative schema")
}

func (b *builder) grants(grants []*ir.Grant, scope string, kind pg.ObjectType) error {
	for _, grant := range grants {
		ref := grant.GetObject()
		if ref.GetKind() == ir.ObjectKind_OBJECT_KIND_UNSPECIFIED &&
			kind != pg.ObjectType_OBJECT_TYPE && kind != pg.ObjectType_OBJECT_DOMAIN {
			return refusal("RTL-C04", grant.GetMeta(), "grant target not allowed")
		}

		qualify(ref.GetName(), scope, ref.GetKind())

		schema := b.schemas[ref.GetName().GetParts()[0]]
		if schema == nil {
			return refusal("RTL-C03", grant.GetMeta(), "undeclared schema "+ref.GetName().GetParts()[0])
		}

		schema.Grants = append(schema.Grants, grant)
	}

	return nil
}

func collectionKind(collection string) ir.ObjectKind {
	switch collection {
	case "tables":
		return ir.ObjectKind_OBJECT_KIND_TABLE
	case "views":
		return ir.ObjectKind_OBJECT_KIND_VIEW
	case "materialized_views":
		return ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW
	case "sequences":
		return ir.ObjectKind_OBJECT_KIND_SEQUENCE
	case "functions":
		return ir.ObjectKind_OBJECT_KIND_FUNCTION
	case "procedures":
		return ir.ObjectKind_OBJECT_KIND_PROCEDURE
	case "enums":
		return ir.ObjectKind_OBJECT_KIND_ENUM_TYPE
	case "domains":
		return ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE
	case "composites":
		return ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE
	case "ranges":
		return ir.ObjectKind_OBJECT_KIND_RANGE_TYPE
	default:
		return ir.ObjectKind_OBJECT_KIND_UNSPECIFIED
	}
}

func namespace(kind ir.ObjectKind) string {
	switch kind {
	case ir.ObjectKind_OBJECT_KIND_TABLE, ir.ObjectKind_OBJECT_KIND_VIEW, ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW,
		ir.ObjectKind_OBJECT_KIND_SEQUENCE, ir.ObjectKind_OBJECT_KIND_INDEX:
		return "relation"
	case ir.ObjectKind_OBJECT_KIND_ENUM_TYPE, ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE,
		ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, ir.ObjectKind_OBJECT_KIND_RANGE_TYPE:
		return "type"
	case ir.ObjectKind_OBJECT_KIND_FUNCTION, ir.ObjectKind_OBJECT_KIND_PROCEDURE:
		return "routine"
	default:
		return kind.String()
	}
}
