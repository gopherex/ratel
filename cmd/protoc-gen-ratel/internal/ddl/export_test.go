package ddl

// Test-only views of unexported helpers, so the naming and expression rules
// are tested table-driven in the external test package.

func SnakeCase(name string) string { return snakeCase(name) }

func QuoteIdent(name string) string { return quoteIdent(name) }

func SQLString(value string) string { return sqlString(value) }

func Exported(name string) bool { return exported(name) }

func ValidateExpr(expr string) error { return validateExpr("expr", expr) }
