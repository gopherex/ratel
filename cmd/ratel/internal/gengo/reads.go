package gengo

import (
	"go/token"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
)

// writeRead generates a :one or :many query: its row type and scanner, a
// column descriptor for SELECT queries, and the query builder steps.
func (s *state) writeRead(file *unit, value *ir.Query, parameters []parameter) error {
	name := value.GetName()

	result, err := s.resultShape(file, value)
	if err != nil {
		return err
	}

	if result.declare != "" {
		if err := file.declare(result.typeName, "query "+name); err != nil {
			return err
		}

		file.body.WriteString(result.declare)
		s.checkShape(value, result)
	}

	scanner := "scan" + name

	if err := file.declare(scanner, "query "+name); err != nil {
		return err
	}

	file.imports = append(file.imports, "github.com/jackc/pgx/v5")
	writeQueryScanner(file, scanner, result)

	columns := make([]string, 0, len(result.columns))
	for _, column := range result.columns {
		columns = append(columns, column.name)
	}

	if value.GetAst().GetSelect() != nil && distinctIdentifiers(columns) {
		if err := s.writeColumns(file, value, result); err != nil {
			return err
		}
	}

	return writeSteps(file, name, "ratel.Query["+result.typeName+"]", parameters, func(arguments string) string {
		call := "ratel.NewQuery(" + quotedList(columns) + ", " + scanner + ", " + strconv.Quote(value.GetSql())
		if arguments != "" {
			call += ", " + arguments
		}

		return call + ")"
	})
}

// checkShape warns when a query declares the same row shape as an earlier
// one: @ratel.row would share one type.
func (s *state) checkShape(value *ir.Query, result shape) {
	if annotationValue(value, "ratel", "row") != "" {
		return
	}

	_, key, found := strings.Cut(result.declare, "struct {")
	if !found {
		return
	}

	if previous, exists := s.shapes[key]; exists {
		s.warnings = append(s.warnings, &ir.Diagnostic{
			Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARNING, Code: "RTL-G10",
			Message: "queries " + previous + " and " + value.GetName() +
				" return the same row shape; share one type with @ratel.row",
			Source: value.GetMeta().GetSource(),
		})

		return
	}

	s.shapes[key] = value.GetName()
}

func writeQueryScanner(file *unit, scanner string, result shape) {
	targets := make([]string, 0, len(result.columns))
	for _, column := range result.columns {
		targets = append(targets, column.target)
	}

	file.printf("func %s(row pgx.Row) (%s, error) {\n\tvar model %s\n\n%s", scanner, result.typeName, result.typeName,
		result.before)
	file.printf("\tif err := row.Scan(%s); err != nil {\n\t\treturn model, err\n\t}\n\n%s\treturn model, nil\n}\n\n",
		strings.Join(targets, ", "), result.after)
}

// writeColumns writes the descriptor <Name>Q of a SELECT query: its columns
// only, for the filters and ordering of query.Select().
func (s *state) writeColumns(file *unit, value *ir.Query, result shape) error {
	name := value.GetName()

	descriptor, descriptorType := name+"Q", camel(name)+"Query"
	for _, identifier := range []string{descriptor, descriptorType} {
		if err := file.declare(identifier, "query "+name); err != nil {
			return err
		}
	}

	var fields, values strings.Builder

	for index := range value.GetColumns() {
		column, err := s.column(file, value, index)
		if err != nil {
			return err
		}

		kind := "Col"
		if column.nullable {
			kind = "NullCol"
		}

		fields.WriteString("\t" + column.field + " ratel." + kind + "[" + result.typeName + ", " +
			file.use(column.goType) + "]\n")
		values.WriteString("\t" + column.field + ": ratel.New" + kind + "[" + result.typeName + ", " +
			file.use(column.goType) + "](" + strconv.Quote(column.name) + "),\n")
	}

	file.printf("// %s describes the columns of the query %s: %s().Select().Where(%s.Column...).\n"+
		"// Clauses added outside apply to the query's result.\nvar %s = %s{\n%s}\n\ntype %s struct {\n%s}\n\n",
		descriptor, name, name, descriptor, descriptor, descriptorType, values.String(), descriptorType, fields.String())

	return nil
}

// distinctIdentifiers reports output names that make distinct Go fields.
func distinctIdentifiers(columns []string) bool {
	seen := make(map[string]bool, len(columns))

	for _, column := range columns {
		field := pascal(column)
		if field == "" || seen[field] || !token.IsIdentifier(field) {
			return false
		}

		seen[field] = true
	}

	return true
}
