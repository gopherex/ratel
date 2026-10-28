package catalog

import (
	"math"
	"slices"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/ir"
)

const (
	smallInteger = "int2"
	integer      = "int4"
	bigInteger   = "int8"
)

func sequenceDefaults(sequence *ir.Sequence, options []*pg.Node) {
	present := make(map[string]bool, len(options))

	for _, node := range options {
		option := node.GetDefElem()
		present[option.GetDefname()] = option.GetArg() != nil
	}

	if sequence.GetDataType() == nil {
		sequence.DataType = &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: bigInteger}
	}

	if !present["increment"] {
		sequence.Increment = 1
	}

	minimum, maximum := int64(1), int64(math.MaxInt64)
	if sequence.GetIncrement() < 0 {
		minimum, maximum = math.MinInt64, -1
	}

	switch sequence.GetDataType().GetPgName() {
	case smallInteger:
		if minimum < 0 {
			minimum = math.MinInt16
		} else {
			maximum = math.MaxInt16
		}
	case integer:
		if minimum < 0 {
			minimum = math.MinInt32
		} else {
			maximum = math.MaxInt32
		}
	default:
	}

	if !present["minvalue"] {
		sequence.MinValue = minimum
	}

	if !present["maxvalue"] {
		sequence.MaxValue = maximum
	}

	if !present["start"] {
		sequence.Start = sequence.GetMinValue()
		if sequence.GetIncrement() < 0 {
			sequence.Start = sequence.GetMaxValue()
		}
	}

	if !present["cache"] {
		sequence.Cache = 1
	}
}

func tableDefaults(table *ir.Table, syntax *pg.CreateStmt) {
	position := 0

	for _, node := range syntax.GetTableElts() {
		columnSyntax := node.GetColumnDef()
		if columnSyntax == nil {
			continue
		}

		column := table.GetColumns()[position]
		position++

		if column.GetIdentity() == nil {
			continue
		}

		options := column.GetIdentity().GetOptions()
		options.DataType = column.GetType()

		for _, constraint := range columnSyntax.GetConstraints() {
			if constraint.GetConstraint().GetContype() == pg.ConstrType_CONSTR_IDENTITY {
				sequenceDefaults(options, constraint.GetConstraint().GetOptions())
			}
		}
	}
}

func (b *builder) expandColumns(table *ir.Table) error {
	for _, column := range table.GetColumns() {
		serial := serialType(column.GetType().GetPgName())
		if serial == "" && column.GetIdentity() == nil {
			continue
		}

		sequence := &ir.Sequence{DataType: column.GetType(), Meta: sourceMetadata(column.GetMeta())}

		if serial != "" {
			column.Type = &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: serial}
			sequence.DataType = column.GetType()
			sequenceDefaults(sequence, nil)
		} else {
			sequence = cloneSequence(column.GetIdentity().GetOptions())
			sequence.Meta = sourceMetadata(column.GetMeta())
		}

		schema := table.GetName().GetParts()[0]
		name := b.uniqueRelationName(schema, table.GetName().GetParts()[1], column.GetName(), "seq")
		sequence.Name = &ir.QualifiedName{Parts: []string{schema, name}}
		sequence.OwnedBy = &ir.ObjectRef{
			Kind: ir.ObjectKind_OBJECT_KIND_COLUMN,
			Name: &ir.QualifiedName{Parts: append(append([]string{}, table.GetName().GetParts()...), column.GetName())},
		}

		if err := b.add(sequence, "sequences", defaultSchema); err != nil {
			return err
		}

		reference := &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_SEQUENCE, Name: sequence.GetName()}
		column.Nullable = false

		if column.GetIdentity() != nil {
			column.GetIdentity().Sequence = reference
		} else {
			// Spelled as PostgreSQL deparses it: pg_catalog is implicit.
			column.DefaultExpr = &ir.Expr{Node: &ir.Expr_FunctionCall{FunctionCall: &ir.FunctionCall{
				Name: &ir.QualifiedName{Parts: []string{"nextval"}},
				Arguments: []*ir.Expr{{Node: &ir.Expr_Literal{Literal: &ir.Literal{
					Value: &ir.Literal_StringValue{StringValue: ObjectID(reference)},
				}}}},
			}}}
		}
	}

	return nil
}

func sourceMetadata(meta *ir.Metadata) *ir.Metadata {
	return &ir.Metadata{Source: meta.GetSource()}
}

func ensureNotNull(table *ir.Table) {
	for _, column := range table.GetColumns() {
		for _, constraint := range table.GetConstraints() {
			if slices.Contains(constraint.GetPrimaryKey().GetColumns(), column.GetName()) {
				column.Nullable = false
			}
		}

		if column.GetNullable() {
			continue
		}

		found := false

		for _, constraint := range table.GetConstraints() {
			if constraint.GetNotNullColumn() == column.GetName() {
				found = true

				break
			}
		}

		if !found {
			table.Constraints = append(table.GetConstraints(), &ir.Constraint{
				Type:      ir.ConstraintType_CONSTRAINT_TYPE_NOT_NULL,
				Body:      &ir.Constraint_NotNullColumn{NotNullColumn: column.GetName()},
				Validated: true, Meta: sourceMetadata(column.GetMeta()),
			})
		}
	}
}

func cloneSequence(value *ir.Sequence) *ir.Sequence {
	return &ir.Sequence{
		DataType: value.GetDataType(), Start: value.GetStart(), Increment: value.GetIncrement(),
		MinValue: value.GetMinValue(), MaxValue: value.GetMaxValue(), Cache: value.GetCache(), Cycle: value.GetCycle(),
	}
}

func serialType(name string) string {
	switch name {
	case "smallserial", "serial2":
		return smallInteger
	case "serial", "serial4":
		return integer
	case "bigserial", "serial8":
		return bigInteger
	default:
		return ""
	}
}

// uniqueRelationName chooses a relation name free in the schema, as
// PostgreSQL's ChooseRelationName does for indexes and owned sequences.
func (b *builder) uniqueRelationName(schema, name1, name2, label string) string {
	return chooseName(name1, name2, label, func(name string) bool {
		reference := &ir.ObjectRef{Name: &ir.QualifiedName{Parts: []string{schema, name}}}
		_, exists := b.namespaces["relation/"+ObjectID(reference)]

		return exists
	})
}
