package gengo

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/ir"
)

const decimalCodecImport = "github.com/jackc/pgx-shopspring-decimal"

// registry records what a package's RegisterTypes sets up on a connection:
// user types to load, Go defaults of enums and composites, and the ratel
// value wrappers used by nullable and optional fields.
type registry struct {
	loads    map[string]int    // qualified PostgreSQL type -> load rank
	defaults map[string]string // Go type -> qualified PostgreSQL type
	values   map[string]string // Go type -> PostgreSQL type, for ratel.RegisterValue
	decimal  bool
}

func newRegistry() *registry {
	return &registry{loads: make(map[string]int), defaults: make(map[string]string), values: make(map[string]string)}
}

// pgName is the type name pgx's LoadType and ratel.RegisterValue take.
func pgName(typ *ir.TypeRef) string {
	if element := typ.GetElement(); element != nil {
		name := pgName(element)
		if dot := strings.LastIndex(name, "."); dot >= 0 {
			return name[:dot+1] + "_" + name[dot+1:]
		}

		return "_" + name
	}

	if typ.GetUdt() != nil {
		return schemaOf(typ.GetUdt().GetName()) + "." + objectOf(typ.GetUdt().GetName())
	}

	return typ.GetPgName()
}

// use records a field type. A wrapped field (ratel.Null or ratel.Opt) also
// needs the value registration of its Go type.
func (s *state) use(file *unit, typ *ir.TypeRef, mapped goType, wrapped bool) {
	element := typ
	for element.GetElement() != nil {
		element = element.GetElement()
	}

	if element.GetPgName() == numericType && element.GetUdt() == nil {
		file.registry.decimal = true
	}

	if isUserType(element) {
		s.load(file, element)
	}

	if wrapped {
		file.registry.values[file.use(mapped)] = s.valueName(typ)
	}
}

// load records a user type and its array, ranked after the types it is
// built from, so LoadType finds them registered.
func (s *state) load(file *unit, element *ir.TypeRef) {
	key := pgName(element)
	if _, done := file.registry.loads[key]; done {
		return
	}

	rank := 0

	for _, dependency := range s.dependencies(key) {
		s.load(file, dependency)
		rank = max(rank, file.registry.loads[pgName(dependency)]+1)
	}

	file.registry.loads[key] = rank
	file.registry.loads[pgName(&ir.TypeRef{Element: element})] = rank + 1

	found := s.types[key]

	goName := found.name
	if found.schema != file.schema {
		goName = packageName(found.schema) + "." + found.name
	}

	array := pgName(&ir.TypeRef{Element: element})

	switch found.kind {
	case ir.TypeKind_TYPE_KIND_ENUM:
		file.registry.defaults[goName+`("")`] = key
	case ir.TypeKind_TYPE_KIND_COMPOSITE:
		file.registry.defaults[goName+"{}"] = key
	default:
		return
	}

	file.registry.defaults["[]"+goName+"(nil)"] = array
	file.registry.defaults["[]*"+goName+"(nil)"] = array
}

// dependencies are the user types a domain, range or composite is built from.
func (s *state) dependencies(key string) []*ir.TypeRef {
	found := s.types[key]

	var direct []*ir.TypeRef

	switch found.kind {
	case ir.TypeKind_TYPE_KIND_DOMAIN, ir.TypeKind_TYPE_KIND_RANGE:
		direct = []*ir.TypeRef{found.base}
	case ir.TypeKind_TYPE_KIND_COMPOSITE:
		direct = found.fields
	default:
	}

	var out []*ir.TypeRef

	for _, typ := range direct {
		for typ.GetElement() != nil {
			typ = typ.GetElement()
		}

		if isUserType(typ) {
			out = append(out, typ)
		}
	}

	return out
}

// valueName is the PostgreSQL type a wrapped Go type registers for. A domain
// registers its base: the domain's Go type is its base's, and one Go wrapper
// has one default PostgreSQL type.
func (s *state) valueName(typ *ir.TypeRef) string {
	if element := typ.GetElement(); element != nil {
		return pgName(&ir.TypeRef{Element: s.valueType(element)})
	}

	return pgName(s.valueType(typ))
}

func (s *state) valueType(typ *ir.TypeRef) *ir.TypeRef {
	for range len(s.types) + 1 {
		found, ok := s.types[pgName(typ)]
		if !ok || found.kind != ir.TypeKind_TYPE_KIND_DOMAIN {
			return typ
		}

		typ = found.base
	}

	return typ
}

func writeRegisterTypes(file *unit) {
	file.imports = append(file.imports, "context", "github.com/jackc/pgx/v5")
	file.printf("// RegisterTypes registers the PostgreSQL types of this package and the\n" +
		"// ratel value wrappers its fields use. Call it on every connection, for\n" +
		"// example from pgxpool.Config.AfterConnect.\n" +
		"func RegisterTypes(ctx context.Context, conn *pgx.Conn) error {\n\ttypes := conn.TypeMap()\n\n")

	if file.registry.decimal {
		file.imports = append(file.imports, decimalCodecImport)
		file.printf("\tpgxdecimal.Register(types)\n\n")
	}

	loads := slices.SortedFunc(maps.Keys(file.registry.loads), func(left, right string) int {
		return cmp.Or(cmp.Compare(file.registry.loads[left], file.registry.loads[right]), cmp.Compare(left, right))
	})
	if len(loads) > 0 {
		file.imports = append(file.imports, "fmt")
		file.printf("\tfor _, name := range %s {\n\t\tloaded, err := conn.LoadType(ctx, name)\n"+
			"\t\tif err != nil {\n\t\t\treturn fmt.Errorf(\"load type %%s: %%w\", name, err)\n\t\t}\n\n"+
			"\t\ttypes.RegisterType(loaded)\n\t}\n\n", quotedList(loads))
	}

	for _, zero := range slices.Sorted(maps.Keys(file.registry.defaults)) {
		file.printf("\ttypes.RegisterDefaultPgType(%s, %s)\n", zero, strconv.Quote(file.registry.defaults[zero]))
	}

	if !file.registry.decimal && len(loads)+len(file.registry.values) == 0 {
		file.printf("\t_ = types\n")
	}

	for _, goName := range slices.Sorted(maps.Keys(file.registry.values)) {
		file.imports = append(file.imports, ratelImport)
		file.printf("\n\tif err := ratel.RegisterValue[%s](types, %s); err != nil {\n\t\treturn err\n\t}\n",
			goName, strconv.Quote(file.registry.values[goName]))
	}

	file.printf("\n\treturn nil\n}\n")
}
