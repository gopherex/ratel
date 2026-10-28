package mapper

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/ir"
)

// MapCreateSchema maps namespace identity, not its embedded declarations.
// The caller visits SchemaElts separately; this package never assembles a catalog.
func (m Mapper) MapCreateSchema(value *pg.CreateSchemaStmt) *ir.Schema {
	if value == nil {
		return nil
	}

	result := &ir.Schema{Name: value.GetSchemaname(), Owner: roleName(value.GetAuthrole())}
	if result.GetName() == "" {
		result.Name = result.GetOwner()
	}

	m.finish(result)

	return result
}

// MapCreateExtension preserves explicitly requested schema and version.
func (m Mapper) MapCreateExtension(value *pg.CreateExtensionStmt) *ir.Extension {
	if value == nil {
		return nil
	}

	result := &ir.Extension{Name: value.GetExtname()}

	for _, node := range value.GetOptions() {
		option := node.GetDefElem()
		switch option.GetDefname() {
		case "schema":
			result.Schema = option.GetArg().GetString_().GetSval()
		case "new_version":
			result.Version = option.GetArg().GetString_().GetSval()
		}
	}

	m.finish(result)

	return result
}

// MapCreatePolicy leaves attachment to CreatePolicyStmt.Table to the caller.
func (m Mapper) MapCreatePolicy(value *pg.CreatePolicyStmt) *ir.Policy {
	if value == nil {
		return nil
	}

	result := &ir.Policy{
		Name: value.GetPolicyName(), Restrictive: !value.GetPermissive(),
		Using: m.expr(value.GetQual()), WithCheck: m.expr(value.GetWithCheck()),
	}

	switch strings.ToUpper(value.GetCmdName()) {
	case "SELECT":
		result.Command = ir.PolicyCommand_POLICY_COMMAND_SELECT
	case "INSERT":
		result.Command = ir.PolicyCommand_POLICY_COMMAND_INSERT
	case "UPDATE":
		result.Command = ir.PolicyCommand_POLICY_COMMAND_UPDATE
	case "DELETE":
		result.Command = ir.PolicyCommand_POLICY_COMMAND_DELETE
	default:
		result.Command = ir.PolicyCommand_POLICY_COMMAND_ALL
	}

	for _, node := range value.GetRoles() {
		result.Roles = append(result.Roles, roleName(node.GetRoleSpec()))
	}

	m.finish(result)

	return result
}

// MapGrant expands an explicit multi-object grant to one IR Grant per object.
// REVOKE, column privileges and grants on all schema objects return nil:
// they have no declarative IR equivalent; a column grant must never widen to a table grant.
func (m Mapper) MapGrant(value *pg.GrantStmt) []*ir.Grant {
	if value == nil || !value.GetIsGrant() || value.GetTargtype() != pg.GrantTargetType_ACL_TARGET_OBJECT {
		return nil
	}

	privileges := make([]string, 0, len(value.GetPrivileges()))

	for _, node := range value.GetPrivileges() {
		if len(node.GetAccessPriv().GetCols()) > 0 {
			return nil
		}

		privileges = append(privileges, strings.ToUpper(node.GetAccessPriv().GetPrivName()))
	}

	if len(privileges) == 0 {
		privileges = []string{"ALL"}
	}

	roles := make([]string, 0, len(value.GetGrantees()))
	for _, node := range value.GetGrantees() {
		roles = append(roles, roleName(node.GetRoleSpec()))
	}

	result := make([]*ir.Grant, 0, len(value.GetObjects()))

	for _, node := range value.GetObjects() {
		grant := &ir.Grant{
			Object: namedObject(value.GetObjtype(), node), Privileges: privileges,
			Roles: roles, WithGrantOption: value.GetGrantOption(),
		}
		m.finish(grant)
		result = append(result, grant)
	}

	return result
}

// MapComment returns an unresolved target and its source-backed comment metadata.
// The catalog builder applies the metadata only after collecting declarations.
func (m Mapper) MapComment(value *pg.CommentStmt) (*ir.ObjectRef, *ir.Metadata) {
	if value == nil {
		return nil, nil
	}

	metadata := m.metadata()
	metadata.Comment = value.GetComment()

	return namedObject(value.GetObjtype(), value.GetObject()), metadata
}

// MapAlterTable maps the allowed additive commands to a table fragment.
// The caller attaches constraints and ORs enabled/forced flags after collection.
// Any non-declarative command rejects the whole fragment by returning nil.
func (m Mapper) MapAlterTable(value *pg.AlterTableStmt) *ir.Table {
	if value == nil {
		return nil
	}

	result := &ir.Table{Name: relationName(value.GetRelation())}

	for _, node := range value.GetCmds() {
		command := node.GetAlterTableCmd()
		switch command.GetSubtype() {
		case pg.AlterTableType_AT_AddConstraint:
			constraint := m.constraint(command.GetDef().GetConstraint(), "")
			if constraint == nil {
				return nil
			}

			result.Constraints = append(result.Constraints, constraint)
		case pg.AlterTableType_AT_EnableRowSecurity:
			result.RlsEnabled = true
		case pg.AlterTableType_AT_ForceRowSecurity:
			result.RlsForced = true
		default:
			return nil
		}
	}

	m.finish(result)

	return result
}

func roleName(value *pg.RoleSpec) string {
	if value == nil {
		return ""
	}

	if value.GetRolename() != "" {
		return value.GetRolename()
	}

	return strings.TrimPrefix(value.GetRoletype().String(), "ROLESPEC_")
}

func namedObject(kind pg.ObjectType, node *pg.Node) *ir.ObjectRef {
	name := qualified(node.GetList().GetItems())
	if node.GetRangeVar() != nil {
		name = relationName(node.GetRangeVar())
	}

	if node.GetString_() != nil {
		name = &ir.QualifiedName{Parts: []string{node.GetString_().GetSval()}}
	}

	if typ := node.GetTypeName(); typ != nil {
		name = qualified(typ.GetNames())
	}

	if routine := node.GetObjectWithArgs(); routine != nil {
		name = qualified(routine.GetObjname())

		signature := make([]string, 0, len(routine.GetObjargs()))
		for _, argument := range routine.GetObjargs() {
			signature = append(signature, typeIdentity(MapType(argument.GetTypeName())))
		}

		name.Parts = append(name.GetParts(), strings.Join(signature, ","))
	}

	return &ir.ObjectRef{Kind: objectKind(kind), Name: name}
}

func objectKind(kind pg.ObjectType) ir.ObjectKind {
	switch kind {
	case pg.ObjectType_OBJECT_TABLE:
		return ir.ObjectKind_OBJECT_KIND_TABLE
	case pg.ObjectType_OBJECT_COLUMN:
		return ir.ObjectKind_OBJECT_KIND_COLUMN
	case pg.ObjectType_OBJECT_SCHEMA:
		return ir.ObjectKind_OBJECT_KIND_SCHEMA
	case pg.ObjectType_OBJECT_SEQUENCE:
		return ir.ObjectKind_OBJECT_KIND_SEQUENCE
	case pg.ObjectType_OBJECT_FUNCTION:
		return ir.ObjectKind_OBJECT_KIND_FUNCTION
	case pg.ObjectType_OBJECT_PROCEDURE:
		return ir.ObjectKind_OBJECT_KIND_PROCEDURE
	case pg.ObjectType_OBJECT_VIEW:
		return ir.ObjectKind_OBJECT_KIND_VIEW
	case pg.ObjectType_OBJECT_MATVIEW:
		return ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW
	case pg.ObjectType_OBJECT_INDEX:
		return ir.ObjectKind_OBJECT_KIND_INDEX
	case pg.ObjectType_OBJECT_TRIGGER:
		return ir.ObjectKind_OBJECT_KIND_TRIGGER
	case pg.ObjectType_OBJECT_POLICY:
		return ir.ObjectKind_OBJECT_KIND_POLICY
	case pg.ObjectType_OBJECT_TABCONSTRAINT:
		return ir.ObjectKind_OBJECT_KIND_CONSTRAINT
	case pg.ObjectType_OBJECT_EXTENSION:
		return ir.ObjectKind_OBJECT_KIND_EXTENSION
	default:
		return ir.ObjectKind_OBJECT_KIND_UNSPECIFIED
	}
}
