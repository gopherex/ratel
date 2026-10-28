package gengo

import (
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
)

// constraintKind is the ratel.ConstraintKind a violation of the constraint
// reports; a primary key violation is a unique violation.
func constraintKind(constraint *ir.Constraint) string {
	switch constraint.GetType() {
	case ir.ConstraintType_CONSTRAINT_TYPE_PRIMARY_KEY, ir.ConstraintType_CONSTRAINT_TYPE_UNIQUE:
		return "ratel.ConstraintUnique"
	case ir.ConstraintType_CONSTRAINT_TYPE_FOREIGN_KEY:
		return "ratel.ConstraintForeignKey"
	case ir.ConstraintType_CONSTRAINT_TYPE_CHECK:
		return "ratel.ConstraintCheck"
	case ir.ConstraintType_CONSTRAINT_TYPE_NOT_NULL:
		return "ratel.ConstraintNotNull"
	case ir.ConstraintType_CONSTRAINT_TYPE_EXCLUSION:
		return "ratel.ConstraintExclusion"
	default:
		return ""
	}
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

func constraintLiteral(schema, table string, constraint *ir.Constraint) string {
	return "ratel.ConstraintError{Kind: " + constraintKind(constraint) + ", Schema: " + strconv.Quote(schema) +
		", Table: " + strconv.Quote(table) + ", Constraint: " + strconv.Quote(constraint.GetName()) +
		", Columns: " + quotedList(constraintColumns(constraint)) + "}"
}

func constraintList(names names, table *ir.Table) string {
	schema := schemaOf(table.GetName())
	items := make([]string, 0, len(table.GetConstraints()))

	for _, constraint := range table.GetConstraints() {
		if constraintKind(constraint) != "" {
			items = append(items, "\n\t\t\t\t"+constraintLiteral(schema, names.table, constraint))
		}
	}

	return strings.Join(items, ",") + ",\n\t\t\t"
}

// writeConstraints declares one sentinel per named constraint, matched with
// errors.Is against the error a write returns.
func writeConstraints(file *unit, schema, table string, names names, source *ir.Table) error {
	for _, constraint := range source.GetConstraints() {
		if constraintKind(constraint) == "" {
			continue
		}

		sentinel := "Err" + pascal(constraint.GetName())
		if err := file.declare(sentinel, "constraint "+constraint.GetName()+" of "+names.table); err != nil {
			return err
		}

		file.printf("// %s matches a violation of %s on %s.%s.\nvar %s = &%s\n\n", sentinel,
			constraint.GetName(), schema, table, sentinel, constraintLiteral(schema, table, constraint))
	}

	return nil
}
