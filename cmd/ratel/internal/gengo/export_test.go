package gengo

// Exported for the external tests of unexported helpers.

func Pascal(name string) string      { return pascal(name) }
func Singular(name string) string    { return singular(name) }
func Plural(name string) string      { return plural(name) }
func PackageName(name string) string { return packageName(name) }
func EnumLabel(label string) string  { return enumLabel(label) }
func Parameter(name string) string   { return parameterName(name) }

// Mapped is a Go type with its imports.
type Mapped struct {
	Expr    string
	Imports []string
}

// Scalar returns the Go type of a builtin; ok is false without a mapping.
func Scalar(name string) (Mapped, bool) {
	mapped, ok := scalar(name)

	return Mapped{Expr: mapped.expr, Imports: mapped.imports}, ok
}

// Override parses an override value.
func Override(value string) Mapped {
	mapped := override(value)

	return Mapped{Expr: mapped.expr, Imports: mapped.imports}
}
