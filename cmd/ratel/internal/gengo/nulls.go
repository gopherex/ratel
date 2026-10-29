package gengo

import (
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
)

// nullTarget reads a nullable value without type registration: Scan fills a
// pgx-native temporary, and the generated code builds the ratel.Null from it.
type nullTarget struct {
	temp  string // Go type of the temporary
	field string // value field of a pgtype temporary; empty for a *T pointer
	value string // Go type of the Null's value
}

// pgtypeOf is the pgtype struct a built-in Go scalar scans into without an
// allocation; its value field is named after the Go type (Int8.Int64).
func pgtypeOf(expr string) (string, bool) {
	switch expr {
	case boolType:
		return "Bool", true
	case "int16":
		return "Int2", true
	case "int32":
		return "Int4", true
	case int64Type:
		return "Int8", true
	case "float32":
		return "Float4", true
	case "float64":
		return "Float8", true
	case stringType:
		return "Text", true
	default:
		return "", false
	}
}

// nullTarget picks a pgtype struct for a built-in scalar of its default Go
// type, and *T, which pgx allocates only for a non-NULL value, for the rest.
func (s *state) nullTarget(file *unit, typ *ir.TypeRef, mapped goType) *nullTarget {
	file.imports = append(file.imports, ratelImport)
	value := file.use(mapped)

	base := s.valueType(typ)
	expected, builtin := scalar(base.GetPgName())

	if target, ok := pgtypeOf(mapped.expr); ok && builtin && !isUserType(base) && base.GetElement() == nil &&
		expected.expr == mapped.expr {
		file.imports = append(file.imports, pgtypeImport)

		return &nullTarget{temp: "pgtype." + target, field: capitalize(mapped.expr), value: value}
	}

	return &nullTarget{temp: "*" + value, value: value}
}

// null is the ratel.Null built from the temporary.
func (t *nullTarget) null(temp string) string {
	if t.field == "" {
		return "ratel.NullFromPtr(" + temp + ")"
	}

	return "ratel.Null[" + t.value + "]{V: " + temp + "." + t.field + ", Valid: " + temp + ".Valid}"
}

// valid is true when the temporary holds a value.
func (t *nullTarget) valid(temp string) string {
	if t.field == "" {
		return temp + " != nil"
	}

	return temp + ".Valid"
}

// value is the temporary's value; it is read only when valid holds.
func (t *nullTarget) valueOf(temp string) string {
	if t.field == "" {
		return "*" + temp
	}

	return temp + "." + t.field
}

// scanColumn is one Scan argument: a field address, or a temporary for a
// nullable field.
type scanColumn struct {
	target string // field address, "&model.Field"
	null   *nullTarget
}

// scanCode returns the Scan arguments and the code before and after Scan
// that declares the temporaries and assigns the nullable fields.
func scanCode(columns []scanColumn) ([]string, string, string) {
	var declared, assigned strings.Builder

	targets := make([]string, 0, len(columns))

	for _, column := range columns {
		if column.null == nil {
			targets = append(targets, column.target)

			continue
		}

		temp := "null" + strconv.Itoa(len(targets))
		targets = append(targets, "&"+temp)
		declared.WriteString("\tvar " + temp + " " + column.null.temp + "\n")
		assigned.WriteString("\t" + strings.TrimPrefix(column.target, "&") + " = " + column.null.null(temp) + "\n")
	}

	if declared.Len() > 0 {
		declared.WriteString("\n")
		assigned.WriteString("\n")
	}

	return targets, declared.String(), assigned.String()
}

// wire is how arrays carry a scalar user type without registration: as its
// built-in base, cast back to the column type in SQL. text marks values of a
// generated string type (enum), which are sent as string.
type wire struct {
	base string // Go source of the base pgx.Identifier; empty sends the column type
	text bool
}

func (s *state) wire(typ *ir.TypeRef) *wire {
	if !isUserType(typ) {
		return nil
	}

	resolved := s.valueType(typ)

	switch {
	case resolved.GetKind() == ir.TypeKind_TYPE_KIND_ENUM:
		return &wire{base: `pgx.Identifier{"pg_catalog", "text"}`, text: true}
	case isUserType(resolved) || resolved.GetElement() != nil:
		return nil // composites and ranges are registered
	default:
		return &wire{base: pgTypeName(resolved)}
	}
}

// sentAsText reports values of a generated string type.
func (c column) sentAsText() bool { return c.wire != nil && c.wire.text }

// projection is the element type of an array sent to pgx and the statement
// that stores one value: nullable values as pointers, generated string types
// as string, so pgx encodes a built-in array type.
func projection(value column, elementType, access string) (string, string) {
	switch {
	case value.sentAsText() && value.nullable:
		return "*string", "if value := " + access + "; value.Valid {\ntext := string(value.V)\nout[index] = &text\n}"
	case value.sentAsText():
		return "string", "out[index] = string(" + access + ")"
	case value.nullable:
		return "*" + elementType, "out[index] = " + access + ".Ptr()"
	default:
		return elementType, "out[index] = " + access
	}
}

// baseField is the Base field of an InsertField or RelKey, when one is needed.
func baseField(value column) string {
	if value.wire == nil || value.wire.base == "" {
		return ""
	}

	return ", Base: " + value.wire.base
}
