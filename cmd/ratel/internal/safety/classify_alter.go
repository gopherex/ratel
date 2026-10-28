package safety

import (
	pg "github.com/pganalyze/pg_query_go/v6"
)

// alterTable is one operation per ALTER TABLE or ALTER INDEX command.
func (c *Classifier) alterTable(statement *pg.AlterTableStmt) []Operation {
	relation := c.name(statement.GetRelation())
	operations := make([]Operation, 0, len(statement.GetCmds()))

	for _, node := range statement.GetCmds() {
		command := node.GetAlterTableCmd()
		operation := Operation{Kind: columnKind(command), Relation: relation}

		switch {
		case operation.Kind != Unknown:
			if operation.Kind == AddColumn && notNullWithoutDefault(command.GetDef().GetColumnDef()) {
				operation.Codes = []string{codeNotNull}
			}
		case command.GetSubtype() == pg.AlterTableType_AT_AddConstraint:
			operation = c.addConstraint(relation, command.GetDef().GetConstraint())
		default:
			operation = c.partition(statement, relation, command)
		}

		operations = append(operations, operation)
	}

	return operations
}

// columnKind classifies the commands on columns and table properties.
func columnKind(command *pg.AlterTableCmd) Kind {
	switch command.GetSubtype() {
	case pg.AlterTableType_AT_AddColumn:
		return AddColumn
	case pg.AlterTableType_AT_DropColumn:
		return DropColumn
	case pg.AlterTableType_AT_AlterColumnType:
		return AlterColumnType
	case pg.AlterTableType_AT_ColumnDefault:
		if command.GetDef() == nil {
			return DropDefault
		}

		return SetDefault
	case pg.AlterTableType_AT_SetNotNull:
		return SetNotNull
	case pg.AlterTableType_AT_DropNotNull:
		return DropNotNull
	case pg.AlterTableType_AT_AddIdentity, pg.AlterTableType_AT_SetIdentity, pg.AlterTableType_AT_DropIdentity:
		return AlterIdentity
	case pg.AlterTableType_AT_SetExpression:
		return SetExpression
	case pg.AlterTableType_AT_DropExpression:
		return DropExpression
	default:
		return tableKind(command.GetSubtype())
	}
}

func tableKind(subtype pg.AlterTableType) Kind {
	switch subtype {
	case pg.AlterTableType_AT_ValidateConstraint:
		return ValidateConstraint
	case pg.AlterTableType_AT_DropConstraint:
		return DropConstraint
	case pg.AlterTableType_AT_SetLogged, pg.AlterTableType_AT_SetUnLogged:
		return SetLogged
	case pg.AlterTableType_AT_SetAccessMethod, pg.AlterTableType_AT_SetTableSpace:
		return RewriteTable
	case pg.AlterTableType_AT_EnableRowSecurity, pg.AlterTableType_AT_DisableRowSecurity,
		pg.AlterTableType_AT_ForceRowSecurity, pg.AlterTableType_AT_NoForceRowSecurity:
		return AlterRLS
	default:
		return Unknown
	}
}

// notNullWithoutDefault reports a column declared NOT NULL with nothing to
// fill existing rows.
func notNullWithoutDefault(column *pg.ColumnDef) bool {
	var notNull, filled bool

	for _, node := range column.GetConstraints() {
		switch node.GetConstraint().GetContype() {
		case pg.ConstrType_CONSTR_NOTNULL, pg.ConstrType_CONSTR_PRIMARY:
			notNull = true
		case pg.ConstrType_CONSTR_DEFAULT, pg.ConstrType_CONSTR_GENERATED, pg.ConstrType_CONSTR_IDENTITY:
			filled = true
		default:
		}
	}

	return notNull && !filled
}

func (c *Classifier) addConstraint(relation string, constraint *pg.Constraint) Operation {
	operation := Operation{Kind: AddCheck, Relation: relation}
	valid := !constraint.GetSkipValidation()

	switch constraint.GetContype() {
	case pg.ConstrType_CONSTR_CHECK:
		if !valid {
			operation.Kind = AddCheckNotValid
		}
	case pg.ConstrType_CONSTR_FOREIGN:
		operation.Kind, operation.Other = AddForeignKey, c.name(constraint.GetPktable())
		if !valid {
			operation.Kind = AddForeignKeyNotValid
		}
	case pg.ConstrType_CONSTR_PRIMARY, pg.ConstrType_CONSTR_UNIQUE:
		operation.Kind = AddUnique
		if constraint.GetIndexname() != "" {
			operation.Kind = AddConstraintIndex
		}
	case pg.ConstrType_CONSTR_EXCLUSION:
		operation.Kind = AddExclusion
	default:
		operation.Kind = Unknown
	}

	return operation
}

// partition classifies ATTACH and DETACH of partitions and partition indexes.
func (c *Classifier) partition(statement *pg.AlterTableStmt, relation string, command *pg.AlterTableCmd) Operation {
	partition := command.GetDef().GetPartitionCmd()
	operation := Operation{Kind: Unknown, Relation: relation, Other: c.name(partition.GetName())}

	switch command.GetSubtype() {
	case pg.AlterTableType_AT_AttachPartition:
		operation.Kind = AttachPartition
		if statement.GetObjtype() == pg.ObjectType_OBJECT_INDEX {
			operation.Kind, operation.Other = AttachIndex, ""
		}
	case pg.AlterTableType_AT_DetachPartition:
		operation.Kind = DetachPartition
		if partition.GetConcurrent() {
			operation.Kind = DetachConcurrently
		}
	case pg.AlterTableType_AT_DetachPartitionFinalize:
		operation.Kind = DetachPartitionFinalize
	default:
		operation.Other = ""
	}

	return operation
}

func (c *Classifier) rename(statement *pg.RenameStmt) []Operation {
	relation := c.name(statement.GetRelation())

	switch statement.GetRenameType() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW,
		pg.ObjectType_OBJECT_SEQUENCE:
		return []Operation{{Kind: RenameTable, Relation: relation}}
	case pg.ObjectType_OBJECT_COLUMN:
		return []Operation{{Kind: RenameColumn, Relation: relation}}
	case pg.ObjectType_OBJECT_TABCONSTRAINT:
		return []Operation{{Kind: RenameConstraint, Relation: relation}}
	case pg.ObjectType_OBJECT_INDEX:
		return []Operation{{Kind: RenameIndex, Relation: relation}}
	case pg.ObjectType_OBJECT_TYPE, pg.ObjectType_OBJECT_DOMAIN:
		return []Operation{{Kind: RenameType}}
	default:
		return []Operation{{Kind: Unknown}}
	}
}

// drop is one operation per dropped object.
func (c *Classifier) drop(statement *pg.DropStmt) []Operation {
	operations := make([]Operation, 0, len(statement.GetObjects()))

	for _, object := range statement.GetObjects() {
		items := object.GetList().GetItems()
		operation := Operation{Kind: dropKind(statement)}

		switch operation.Kind {
		case DropTable, DropSequence, DropView:
			operation.Relation = c.qualified(items)
		case DropIndex, DropIndexConcurrently:
			operation.Relation = c.env.Indexes[c.qualified(items)]
		case DropTrigger, DropPolicy:
			operation.Relation = c.qualified(items[:len(items)-1])
		default:
		}

		operations = append(operations, operation)
	}

	return operations
}

func dropKind(statement *pg.DropStmt) Kind {
	switch statement.GetRemoveType() {
	case pg.ObjectType_OBJECT_SCHEMA:
		return DropSchema
	case pg.ObjectType_OBJECT_EXTENSION:
		return DropExtension
	case pg.ObjectType_OBJECT_TYPE, pg.ObjectType_OBJECT_DOMAIN:
		return DropType
	case pg.ObjectType_OBJECT_SEQUENCE:
		return DropSequence
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE, pg.ObjectType_OBJECT_ROUTINE:
		return DropRoutine
	case pg.ObjectType_OBJECT_TABLE:
		return DropTable
	case pg.ObjectType_OBJECT_INDEX:
		if statement.GetConcurrent() {
			return DropIndexConcurrently
		}

		return DropIndex
	case pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW:
		return DropView
	case pg.ObjectType_OBJECT_TRIGGER:
		return DropTrigger
	case pg.ObjectType_OBJECT_POLICY:
		return DropPolicy
	default:
		return Unknown
	}
}
