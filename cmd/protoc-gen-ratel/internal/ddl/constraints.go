package ddl

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/options"
)

// keyElements renders the table-level PRIMARY KEY, UNIQUE and CHECK
// constraints, in that order.
func keyElements(msg protoreflect.MessageDescriptor, opt *options.Table) ([]element, error) {
	var elements []element

	if keys := opt.GetPrimaryKey(); len(keys) > 0 {
		columns, err := directColumns(msg, keys)
		if err != nil {
			return nil, err
		}

		elements = append(elements, element{body: "PRIMARY KEY (" + joinIdents(columns) + ")"})
	}

	for _, unique := range opt.GetUniques() {
		columns, err := directColumns(msg, unique.GetFields())
		if err != nil {
			return nil, err
		}

		name := unique.GetName()
		if name == "" {
			name = defaultName(msg, columns, "key")
		}

		elements = append(elements, element{
			body: "CONSTRAINT " + quoteIdent(name) + " UNIQUE (" + joinIdents(columns) + ")",
		})
	}

	for _, check := range opt.GetChecks() {
		if check.GetName() == "" {
			return nil, fmt.Errorf("%w: a check needs a name", ErrInvalidOption)
		}

		if err := validateExpr("check", check.GetExpr()); err != nil {
			return nil, err
		}

		elements = append(elements, element{
			body: "CONSTRAINT " + quoteIdent(check.GetName()) + " CHECK (" + check.GetExpr() + ")",
		})
	}

	return elements, nil
}

// indexStatements renders one CREATE INDEX per index option.
func indexStatements(msg protoreflect.MessageDescriptor, opt *options.Table, qualified string) ([]string, error) {
	statements := make([]string, 0, len(opt.GetIndexes()))

	for _, index := range opt.GetIndexes() {
		columns, err := directColumns(msg, index.GetFields())
		if err != nil {
			return nil, err
		}

		name := index.GetName()
		if name == "" {
			name = defaultName(msg, columns, "idx")
		}

		head := "CREATE INDEX "
		if index.GetUnique() {
			head = "CREATE UNIQUE INDEX "
		}

		statement := head + quoteIdent(name) + " ON " + qualified + " (" + joinIdents(columns) + ")"

		if where := index.GetWhere(); where != "" {
			if err := validateExpr("index predicate", where); err != nil {
				return nil, err
			}

			statement += " WHERE " + where
		}

		statements = append(statements, statement+";")
	}

	return statements, nil
}

// defaultName builds the name of an unnamed index or unique constraint from
// its table and columns, in the style PostgreSQL uses.
func defaultName(msg protoreflect.MessageDescriptor, columns []string, suffix string) string {
	return tableName(msg) + "_" + strings.Join(columns, "_") + "_" + suffix
}

// validateExpr rejects a raw SQL fragment that cannot sit inside a generated
// statement: it must be one non-empty line with no statement end and no
// comment opener. Fragments are inserted verbatim, so this is the only guard.
func validateExpr(what, expr string) error {
	if expr == "" || strings.ContainsAny(expr, "\r\n;") || strings.Contains(expr, "--") ||
		strings.Contains(expr, "/*") {
		return fmt.Errorf("%w: %s %q must be one SQL expression on one line without ; -- or /*",
			ErrInvalidOption, what, expr)
	}

	return nil
}
