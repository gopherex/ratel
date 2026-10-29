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

// ScanColumn is a Scan argument: a field address and, for a nullable field,
// the temporary's Go type and pgtype value field ("" for a *T pointer).
type ScanColumn struct{ Target, Temp, Field, Value string }

// ScanCode returns the Scan arguments and the code around Scan.
func ScanCode(columns []ScanColumn) ([]string, string, string) {
	scanned := make([]scanColumn, len(columns))
	for index, value := range columns {
		scanned[index] = scanColumn{target: value.Target}
		if value.Temp != "" {
			scanned[index].null = &nullTarget{temp: value.Temp, field: value.Field, value: value.Value}
		}
	}

	return scanCode(scanned)
}

// Projection is the array element type and store statement of a value.
func Projection(text, nullable bool, elementType, access string) (string, string) {
	value := column{nullable: nullable}
	if text {
		value.wire = &wire{text: true}
	}

	return projection(value, elementType, access)
}

// Override parses an override value.
func Override(value string) Mapped {
	mapped := override(value)

	return Mapped{Expr: mapped.expr, Imports: mapped.imports}
}
