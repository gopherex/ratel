package safety

import (
	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/cmd/ratel/internal/ident"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// Classifier turns hand-written statements into operations, naming
// relations as introspection renders them so they match Env.
type Classifier struct {
	env    Env
	quoter *ident.Quoter
}

// NewClassifier classifies statements against env; parser decides which
// names are keywords that need quoting.
func NewClassifier(parser parse.Parser, env Env) *Classifier {
	return &Classifier{env: env, quoter: ident.New(parser)}
}

// Operations is what one parsed statement does. Statements that change
// nothing, such as SET, SELECT and transaction control, have none.
func (c *Classifier) Operations(node *pg.Node) []Operation {
	if operations, ok := c.objects(node); ok {
		return operations
	}

	if operations, ok := c.relations(node); ok {
		return operations
	}

	switch {
	case node.GetAlterTableStmt() != nil:
		return c.alterTable(node.GetAlterTableStmt())
	case node.GetRenameStmt() != nil:
		return c.rename(node.GetRenameStmt())
	case node.GetDropStmt() != nil:
		return c.drop(node.GetDropStmt())
	case node.GetAlterDomainStmt() != nil:
		return []Operation{{Kind: domainKind(node.GetAlterDomainStmt())}}
	case node.GetVariableSetStmt() != nil, node.GetTransactionStmt() != nil, node.GetSelectStmt() != nil:
		return nil
	default:
		return []Operation{{Kind: Unknown}}
	}
}

// objects classifies statements on objects that are not relations.
func (c *Classifier) objects(node *pg.Node) ([]Operation, bool) {
	var kind Kind

	switch {
	case node.GetCreateSchemaStmt() != nil:
		kind = CreateSchema
	case node.GetCreateExtensionStmt() != nil:
		kind = CreateExtension
	case node.GetAlterExtensionStmt() != nil:
		kind = AlterExtension
	case node.GetCreateEnumStmt() != nil, node.GetCompositeTypeStmt() != nil, node.GetCreateDomainStmt() != nil,
		node.GetCreateRangeStmt() != nil:
		kind = CreateType
	case node.GetAlterEnumStmt() != nil:
		kind = AddEnumValue
		if node.GetAlterEnumStmt().GetOldVal() != "" {
			kind = RenameEnumValue
		}
	case node.GetCreateFunctionStmt() != nil:
		kind = CreateRoutine
		if node.GetCreateFunctionStmt().GetReplace() {
			kind = ReplaceRoutine
		}
	case node.GetCreateSeqStmt() != nil:
		return []Operation{{Kind: CreateSequence, Relation: c.name(node.GetCreateSeqStmt().GetSequence())}}, true
	case node.GetAlterSeqStmt() != nil:
		return []Operation{{Kind: AlterSequence, Relation: c.name(node.GetAlterSeqStmt().GetSequence())}}, true
	default:
		return nil, false
	}

	return []Operation{{Kind: kind}}, true
}

// relations classifies statements creating relations or touching their
// data and privileges.
func (c *Classifier) relations(node *pg.Node) ([]Operation, bool) {
	switch {
	case node.GetCreateStmt() != nil:
		return c.createTable(node.GetCreateStmt()), true
	case node.GetIndexStmt() != nil:
		return []Operation{c.createIndex(node.GetIndexStmt())}, true
	case node.GetViewStmt() != nil:
		kind := CreateView
		if node.GetViewStmt().GetReplace() {
			kind = ReplaceView
		}

		return []Operation{{Kind: kind, Relation: c.name(node.GetViewStmt().GetView())}}, true
	case node.GetCreateTableAsStmt() != nil:
		return []Operation{{Kind: CreateView, Relation: c.name(node.GetCreateTableAsStmt().GetInto().GetRel())}}, true
	case node.GetRefreshMatViewStmt() != nil:
		return []Operation{{Kind: RefreshView, Relation: c.name(node.GetRefreshMatViewStmt().GetRelation())}}, true
	case node.GetReindexStmt() != nil:
		return []Operation{c.reindex(node.GetReindexStmt())}, true
	case node.GetCreateTrigStmt() != nil:
		return []Operation{{Kind: CreateTrigger, Relation: c.name(node.GetCreateTrigStmt().GetRelation())}}, true
	case node.GetCreatePolicyStmt() != nil:
		return []Operation{{Kind: CreatePolicy, Relation: c.name(node.GetCreatePolicyStmt().GetTable())}}, true
	case node.GetAlterPolicyStmt() != nil:
		return []Operation{{Kind: CreatePolicy, Relation: c.name(node.GetAlterPolicyStmt().GetTable())}}, true
	default:
		return c.data(node)
	}
}

// data classifies statements on table contents, privileges and comments.
func (c *Classifier) data(node *pg.Node) ([]Operation, bool) {
	var relation *pg.RangeVar

	switch {
	case node.GetCommentStmt() != nil:
		return []Operation{{Kind: Comment, Relation: c.commented(node.GetCommentStmt())}}, true
	case node.GetGrantStmt() != nil:
		return c.each(Grant, node.GetGrantStmt().GetObjects()), true
	case node.GetTruncateStmt() != nil:
		return c.each(Truncate, node.GetTruncateStmt().GetRelations()), true
	case node.GetClusterStmt() != nil:
		return []Operation{{Kind: RewriteTable, Relation: c.name(node.GetClusterStmt().GetRelation())}}, true
	case node.GetInsertStmt() != nil:
		relation = node.GetInsertStmt().GetRelation()
	case node.GetUpdateStmt() != nil:
		relation = node.GetUpdateStmt().GetRelation()
	case node.GetDeleteStmt() != nil:
		relation = node.GetDeleteStmt().GetRelation()
	case node.GetMergeStmt() != nil:
		relation = node.GetMergeStmt().GetRelation()
	default:
		return nil, false
	}

	return []Operation{{Kind: DataChange, Relation: c.name(relation)}}, true
}

// createTable creates the table and locks what its foreign keys reference
// and the parent it is a partition of.
func (c *Classifier) createTable(statement *pg.CreateStmt) []Operation {
	table := c.name(statement.GetRelation())
	operations := []Operation{{Kind: CreateTable, Relation: table}}

	if statement.GetPartbound() != nil {
		for _, parent := range statement.GetInhRelations() {
			operations = append(operations, Operation{
				Kind: CreatePartition, Relation: c.name(parent.GetRangeVar()), Other: table,
			})
		}
	}

	for _, element := range statement.GetTableElts() {
		constraints := element.GetColumnDef().GetConstraints()
		if element.GetConstraint() != nil {
			constraints = append(constraints, element)
		}

		for _, constraint := range constraints {
			if referenced := constraint.GetConstraint().GetPktable(); referenced != nil {
				operations = append(operations, Operation{Kind: CreateTable, Relation: table, Other: c.name(referenced)})
			}
		}
	}

	return operations
}

func (c *Classifier) createIndex(statement *pg.IndexStmt) Operation {
	operation := Operation{Kind: CreateIndex, Relation: c.name(statement.GetRelation())}

	switch {
	case statement.GetConcurrent():
		operation.Kind = CreateIndexConcurrently
	case !statement.GetRelation().GetInh():
		operation.Kind = CreateIndexOnly
	default:
	}

	return operation
}

func (c *Classifier) reindex(statement *pg.ReindexStmt) Operation {
	operation := Operation{Kind: Reindex, Relation: c.name(statement.GetRelation())}

	if statement.GetKind() == pg.ReindexObjectType_REINDEX_OBJECT_INDEX {
		operation.Relation = c.env.Indexes[operation.Relation]
	}

	for _, parameter := range statement.GetParams() {
		if parameter.GetDefElem().GetDefname() == "concurrently" {
			operation.Kind = ReindexConcurrently
		}
	}

	return operation
}

// commented is the relation a comment is on, when it is on one.
func (c *Classifier) commented(statement *pg.CommentStmt) string {
	items := statement.GetObject().GetList().GetItems()

	switch statement.GetObjtype() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW:
		return c.qualified(items)
	case pg.ObjectType_OBJECT_COLUMN:
		return c.qualified(items[:len(items)-1])
	default:
		return ""
	}
}

// each is one operation per relation of a statement.
func (c *Classifier) each(kind Kind, relations []*pg.Node) []Operation {
	operations := make([]Operation, 0, len(relations))
	for _, relation := range relations {
		operations = append(operations, Operation{Kind: kind, Relation: c.name(relation.GetRangeVar())})
	}

	return operations
}

func domainKind(statement *pg.AlterDomainStmt) Kind {
	switch statement.GetSubtype() {
	case "O":
		return DomainNotNull
	case "C":
		if statement.GetDef().GetConstraint().GetSkipValidation() {
			return DomainCheckNotValid
		}

		return DomainCheck
	case "V":
		return DomainValidate
	default:
		return AlterDomain
	}
}

// name is a relation as "schema.name", quoted as PostgreSQL's quote_ident
// quotes; an unqualified name is taken to be in public.
func (c *Classifier) name(relation *pg.RangeVar) string {
	if relation == nil {
		return ""
	}

	schema := relation.GetSchemaname()
	if schema == "" {
		schema = "public"
	}

	return c.quoter.Qualified(schema, relation.GetRelname())
}

// qualified is a "schema.name" from a list of name parts.
func (c *Classifier) qualified(items []*pg.Node) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.GetString_().GetSval())
	}

	if len(parts) == 1 {
		parts = append([]string{"public"}, parts...)
	}

	return c.quoter.Qualified(parts...)
}
