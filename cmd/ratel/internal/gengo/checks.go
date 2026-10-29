package gengo

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/pkg/ir"
)

// checkLabels returns the values of a CHECK (column IN ('a', 'b', ...)) on a
// text column, which becomes a Go enum like a PostgreSQL enum does.
func checkLabels(table *ir.Table, column *ir.Column) []string {
	switch column.GetType().GetPgName() {
	case "text", "varchar", "bpchar":
	default:
		return nil
	}

	if column.GetType().GetUdt() != nil {
		return nil
	}

	for _, constraint := range table.GetConstraints() {
		operator := constraint.GetCheck().GetExpression().GetOperator()
		if operator.GetSymbol() != "IN" || len(operator.GetOperands()) != 2 ||
			operator.GetOperands()[0].GetColumnRef().GetColumn() != column.GetName() {
			continue
		}

		var labels []string

		for _, element := range operator.GetOperands()[1].GetList().GetElements() {
			switch value := element.GetLiteral().GetValue().(type) {
			case *ir.Literal_StringValue:
				labels = append(labels, value.StringValue)
			default:
				return nil
			}
		}

		return labels
	}

	return nil
}

// checkEnums gives every checked text column without @go.type its Go enum.
func (s *state) checkEnums(file *unit, names identifiers, table *ir.Table, columns []column) error {
	for index, declared := range table.GetColumns() {
		if _, overridden := s.columns[strings.Join(append(slices.Clone(table.GetName().GetParts()),
			declared.GetName()), ".")]; overridden {
			continue
		}

		labels := checkLabels(table, declared)
		if len(labels) == 0 {
			continue
		}

		mapped, err := writeCheckEnum(file, names, declared, labels)
		if err != nil {
			return err
		}

		columns[index].goType = mapped
		columns[index].wire = &wire{text: true}
	}

	return nil
}

// writeCheckEnum declares the Go enum of a checked text column.
func writeCheckEnum(file *unit, names identifiers, column *ir.Column, labels []string) (goType, error) {
	typeName := names.model + pascal(column.GetName())
	source := "check of column " + names.table + "." + column.GetName()

	if err := file.declare(typeName, source); err != nil {
		return goType{}, err
	}

	file.printf("// %s is the CHECK (%s IN (...)) of %s.\ntype %s string\n\nconst (\n", typeName,
		column.GetName(), names.table, typeName)

	for _, label := range labels {
		constant := typeName + enumLabel(label)
		if err := file.declare(constant, source+" label "+label); err != nil {
			return goType{}, err
		}

		file.printf("\t%s %s = %s\n", constant, typeName, strconv.Quote(label))
	}

	file.printf(")\n\n")

	return goType{expr: typeName}, nil
}
