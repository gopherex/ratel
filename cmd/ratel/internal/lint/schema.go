package lint

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/ir"
)

// Schema lints the desired schema for practices: foreign keys without an
// index on their columns, and column types that age badly. A table or
// column annotated @ratel.allow with the code allows it.
func Schema(catalog *ir.Catalog) []Finding {
	var findings []Finding

	for _, schema := range catalog.GetSchemas() {
		for _, table := range schema.GetTables() {
			findings = append(findings, foreignKeys(table)...)
			findings = append(findings, columnTypes(table)...)
		}
	}

	return findings
}

func foreignKeys(table *ir.Table) []Finding {
	var findings []Finding

	for _, constraint := range table.GetConstraints() {
		key := constraint.GetForeignKey()
		if key == nil || indexed(table, key.GetColumns()) {
			continue
		}

		findings = append(findings, Finding{
			Code: "RTL-P01", Where: "constraint " + qualified(table) + "." + constraint.GetName(),
			Detail:  detail("RTL-P01") + ": " + strings.Join(key.GetColumns(), ", "),
			Allowed: allows(table.GetMeta(), "RTL-P01"),
		})
	}

	return findings
}

// indexed reports an index or key whose leading columns are the columns,
// in any order: the index serves the referencing side's lookups.
func indexed(table *ir.Table, columns []string) bool {
	leads := func(leading []string) bool {
		if len(leading) < len(columns) {
			return false
		}

		for _, column := range columns {
			if !slices.Contains(leading[:len(columns)], column) {
				return false
			}
		}

		return true
	}

	for _, index := range table.GetIndexes() {
		elements := make([]string, 0, len(index.GetElements()))
		for _, element := range index.GetElements() {
			elements = append(elements, element.GetColumn())
		}

		if index.GetPredicate() == nil && leads(elements) {
			return true
		}
	}

	for _, constraint := range table.GetConstraints() {
		if leads(constraint.GetPrimaryKey().GetColumns()) || leads(constraint.GetUnique().GetColumns()) {
			return true
		}
	}

	return false
}

func columnTypes(table *ir.Table) []Finding {
	var primary []string

	findings := make([]Finding, 0, len(table.GetColumns()))

	for _, constraint := range table.GetConstraints() {
		primary = append(primary, constraint.GetPrimaryKey().GetColumns()...)
	}

	for _, column := range table.GetColumns() {
		var problem string

		switch name := column.GetType().GetPgName(); {
		case serial(column):
			problem = "serial column; use an identity"
		case name == "int4" && slices.Contains(primary, column.GetName()):
			problem = "int4 primary key; use int8"
		case name == "timestamp":
			problem = "timestamp without time zone; use timestamptz"
		case name == "bpchar":
			problem = "char(n); use text or varchar"
		default:
			continue
		}

		findings = append(findings, Finding{
			Code: "RTL-P02", Where: "column " + qualified(table) + "." + column.GetName(), Detail: problem,
			Allowed: allows(column.GetMeta(), "RTL-P02") || allows(table.GetMeta(), "RTL-P02"),
		})
	}

	return findings
}

// serial reports a column defaulting to nextval without being an identity.
func serial(column *ir.Column) bool {
	call := column.GetDefaultExpr().GetFunctionCall()
	parts := call.GetName().GetParts()

	return column.GetIdentity() == nil && len(parts) > 0 && parts[len(parts)-1] == "nextval"
}

func qualified(table *ir.Table) string { return strings.Join(table.GetName().GetParts(), ".") }

// allows reports an @ratel.allow annotation for the code.
func allows(meta *ir.Metadata, code string) bool {
	for _, annotation := range meta.GetAnnotations() {
		if annotation.GetNamespace() != "ratel" || annotation.GetName() != "allow" {
			continue
		}

		for _, argument := range annotation.GetArgs() {
			if argument.GetKey() == "" && argument.GetValue() == code {
				return true
			}
		}
	}

	return false
}
