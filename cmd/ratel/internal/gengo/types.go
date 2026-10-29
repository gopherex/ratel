package gengo

import (
	"strings"

	"github.com/gopherex/ratel/pkg/ir"
)

const (
	pgtypeImport  = "github.com/jackc/pgx/v5/pgtype"
	decimalImport = "github.com/shopspring/decimal"
	uuidImport    = "github.com/google/uuid"
	ratelImport   = "github.com/gopherex/ratel/pkg/ratel"

	numericType = "numeric"
	boolType    = "bool" // the PostgreSQL and the Go name
	stringType  = "string"
)

// goType is a Go type expression and the import paths it needs.
type goType struct {
	expr    string
	imports []string
}

// scalar maps a builtin PostgreSQL type to Go; ok is false for a type without
// a mapping, which a configured override must supply.
func scalar(name string) (goType, bool) {
	if mapped, ok := numeric(name); ok {
		return mapped, true
	}

	switch name {
	case "text", "varchar", "bpchar", "char", "name", "citext":
		return goType{expr: stringType}, true
	case boolType:
		return goType{expr: boolType}, true
	case "bytea":
		return goType{expr: "[]byte"}, true
	case "uuid":
		return goType{expr: "uuid.UUID", imports: []string{uuidImport}}, true
	case "timestamptz", "timestamp", "date":
		return goType{expr: "time.Time", imports: []string{"time"}}, true
	case "inet":
		return goType{expr: "netip.Addr", imports: []string{"net/netip"}}, true
	case "cidr":
		return goType{expr: "netip.Prefix", imports: []string{"net/netip"}}, true
	case "json", "jsonb":
		return goType{expr: "json.RawMessage", imports: []string{"encoding/json"}}, true
	case "macaddr", "macaddr8":
		return goType{expr: "net.HardwareAddr", imports: []string{"net"}}, true
	default:
		return pgtypeScalar(name)
	}
}

func numeric(name string) (goType, bool) {
	switch name {
	case "int2":
		return goType{expr: "int16"}, true
	case "int4":
		return goType{expr: "int32"}, true
	case "int8":
		return goType{expr: int64Type}, true
	case "float4":
		return goType{expr: "float32"}, true
	case "float8":
		return goType{expr: "float64"}, true
	case numericType:
		return goType{expr: "decimal.Decimal", imports: []string{decimalImport}}, true
	case "oid", "xid", "cid":
		return goType{expr: "uint32"}, true
	default:
		return goType{}, false
	}
}

// pgtypeScalar maps types whose Go form is a pgtype struct.
func pgtypeScalar(name string) (goType, bool) {
	var expr string

	switch name {
	case "time":
		expr = "pgtype.Time"
	case "interval":
		expr = "pgtype.Interval"
	case "point":
		expr = "pgtype.Point"
	case "line":
		expr = "pgtype.Line"
	case "lseg":
		expr = "pgtype.Lseg"
	case "box":
		expr = "pgtype.Box"
	case "path":
		expr = "pgtype.Path"
	case "polygon":
		expr = "pgtype.Polygon"
	case "circle":
		expr = "pgtype.Circle"
	case "bit", "varbit":
		expr = "pgtype.Bits"
	case "tid":
		expr = "pgtype.TID"
	default:
		return builtinRange(name)
	}

	return goType{expr: expr, imports: []string{pgtypeImport}}, true
}

// builtinRange maps builtin ranges and multiranges to pgtype.Range of the
// subtype's Go type.
func builtinRange(name string) (goType, bool) {
	subtypes := map[string]string{
		"int4range": "int4", "int8range": "int8", "numrange": numericType,
		"tsrange": "timestamp", "tstzrange": "timestamptz", "daterange": "date",
	}

	multi := strings.Replace(name, "multirange", "range", 1)
	if subtype, ok := subtypes[multi]; ok && multi != name {
		element, _ := scalar(subtype)

		return rangeOf(element, true), true
	}

	if subtype, ok := subtypes[name]; ok {
		element, _ := scalar(subtype)

		return rangeOf(element, false), true
	}

	return goType{}, false
}

func rangeOf(element goType, multirange bool) goType {
	expr := "pgtype.Range[" + element.expr + "]"
	if multirange {
		expr = "pgtype.Multirange[" + expr + "]"
	}

	return goType{expr: expr, imports: append([]string{pgtypeImport}, element.imports...)}
}

// override parses an option or @go.type value: a builtin Go type ("string"),
// or an import path and type name joined by the last dot
// ("github.com/acme/md.Markdown").
func override(value string) goType {
	slash := strings.LastIndex(value, "/")

	dot := strings.LastIndex(value, ".")
	if dot <= slash {
		return goType{expr: value}
	}

	path := value[:dot]

	return goType{expr: importName(path) + "." + value[dot+1:], imports: []string{path}}
}

// isUserType reports whether a type reference names a generated type.
func isUserType(t *ir.TypeRef) bool {
	switch t.GetKind() {
	case ir.TypeKind_TYPE_KIND_ENUM, ir.TypeKind_TYPE_KIND_DOMAIN, ir.TypeKind_TYPE_KIND_COMPOSITE,
		ir.TypeKind_TYPE_KIND_RANGE:
		return t.GetUdt() != nil
	default:
		return false
	}
}
