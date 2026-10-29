package mapper

import (
	"math"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/pkg/ir"
)

// MapType preserves PostgreSQL parser-normalized names and declared modifiers.
// User-defined categories and unqualified names require catalog resolution.
func MapType(typ *pg.TypeName) *ir.TypeRef {
	names := stringsOf(typ.GetNames())
	if len(names) == 0 {
		return &ir.TypeRef{}
	}

	name := names[len(names)-1]

	scalar := &ir.TypeRef{
		Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: name,
		Modifier: typeModifier(name, typ.GetTypmods()),
	}
	if len(names) > 1 && names[0] != "pg_catalog" {
		scalar.Kind = ir.TypeKind_TYPE_KIND_USER_DEFINED
		scalar.Udt = &ir.ObjectRef{Name: &ir.QualifiedName{Parts: names}}
	}

	if len(typ.GetArrayBounds()) == 0 {
		return scalar
	}

	return &ir.TypeRef{
		Kind: ir.TypeKind_TYPE_KIND_ARRAY, PgName: "_" + name,
		ArrayDimensions: boundedUint(len(typ.GetArrayBounds())), Element: scalar,
	}
}

func boundedUint(value int) uint32 {
	if value < 0 {
		return 0
	}

	if uint64(value) > math.MaxUint32 {
		return math.MaxUint32
	}

	return uint32(value)
}

func modifierValue(nodes []*pg.Node, index int) uint32 {
	if index >= len(nodes) {
		return 0
	}

	return boundedUint(int(nodes[index].GetAConst().GetIval().GetIval()))
}

func typeModifier(name string, nodes []*pg.Node) *ir.TypeModifier {
	timezone := name == "timestamptz" || name == "timetz"
	if len(nodes) == 0 && !timezone {
		return nil
	}

	first := modifierValue(nodes, 0)

	switch name {
	case "numeric":
		// The IR scale is unsigned. Negative scales stay in the enclosing source SQL.
		if len(nodes) > 1 && nodes[1].GetAConst().GetIval().GetIval() < 0 {
			return nil
		}

		return &ir.TypeModifier{Modifier: &ir.TypeModifier_Numeric{Numeric: &ir.NumericModifier{
			Precision: first, Scale: modifierValue(nodes, 1),
		}}}
	case "varchar", "bpchar", "char", "bit", "varbit":
		return &ir.TypeModifier{Modifier: &ir.TypeModifier_Text{Text: &ir.StringModifier{Length: first}}}
	case "timestamp", "timestamptz", "time", "timetz":
		return &ir.TypeModifier{Modifier: &ir.TypeModifier_DateTime{DateTime: &ir.DateTimeModifier{
			WithTimezone: timezone, Precision: first,
		}}}
	case "interval":
		return &ir.TypeModifier{Modifier: &ir.TypeModifier_Interval{Interval: &ir.IntervalModifier{
			Fields: intervalFields(first), Precision: modifierValue(nodes, 1),
		}}}
	default:
		return nil
	}
}

func intervalFields(mask uint32) string {
	// PostgreSQL datetime.h DTK_M(YEAR/MONTH/DAY/HOUR/MINUTE/SECOND).
	units := []struct {
		bit  uint32
		name string
	}{
		{1 << 2, "YEAR"},
		{1 << 1, "MONTH"},
		{1 << 3, "DAY"},
		{1 << 10, "HOUR"},
		{1 << 11, "MINUTE"},
		{1 << 12, "SECOND"},
	}

	const fullRange = 0x7fff
	if mask == fullRange {
		return ""
	}

	var names []string

	for _, unit := range units {
		if mask&unit.bit != 0 {
			names = append(names, unit.name)
		}
	}

	if len(names) == 0 {
		return ""
	}

	if len(names) == 1 {
		return names[0]
	}

	return names[0] + " TO " + names[len(names)-1]
}

func sortOrder(order pg.SortByDir) ir.SortOrder {
	switch order {
	case pg.SortByDir_SORTBY_ASC:
		return ir.SortOrder_SORT_ORDER_ASC
	case pg.SortByDir_SORTBY_DESC:
		return ir.SortOrder_SORT_ORDER_DESC
	default:
		return ir.SortOrder_SORT_ORDER_UNSPECIFIED
	}
}

func nullsOrder(order pg.SortByNulls) ir.NullsOrder {
	switch order {
	case pg.SortByNulls_SORTBY_NULLS_FIRST:
		return ir.NullsOrder_NULLS_ORDER_FIRST
	case pg.SortByNulls_SORTBY_NULLS_LAST:
		return ir.NullsOrder_NULLS_ORDER_LAST
	default:
		return ir.NullsOrder_NULLS_ORDER_UNSPECIFIED
	}
}

func dotted(nodes []*pg.Node) string { return strings.Join(stringsOf(nodes), ".") }
