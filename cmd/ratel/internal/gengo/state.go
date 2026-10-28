package gengo

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
)

// userType is a generated Go type of a PostgreSQL enum, domain, composite or
// range, placed in its schema's package.
type userType struct {
	schema string
	name   string // Go identifier
	kind   ir.TypeKind
	base   *ir.TypeRef   // domain base or range subtype
	fields []*ir.TypeRef // composite field types
}

// codeAnnotation is the diagnostic code of an invalid go annotation.
const codeAnnotation = "RTL-G03"

type state struct {
	options  Options
	catalog  *ir.Catalog
	types    map[string]userType // by "schema.name"
	columns  map[string]string   // @go.type by "schema.table.column"
	packages map[string]string   // query name -> Go package of its group
	shapes   map[string]string   // row type body -> first query declaring it
	warnings []*ir.Diagnostic
}

func newState(options Options, request *pluginpb.GenerateRequest) (*state, error) {
	result := &state{
		options: options, catalog: request.GetCatalog(),
		types: make(map[string]userType), columns: make(map[string]string), packages: make(map[string]string),
		shapes: make(map[string]string),
	}

	for _, schema := range result.catalog.GetSchemas() {
		result.indexTypes(schema)
	}

	for _, annotation := range request.GetAnnotations() {
		if err := result.annotate(annotation); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (s *state) indexTypes(schema *ir.Schema) {
	add := func(name *ir.QualifiedName, kind ir.TypeKind, base *ir.TypeRef) {
		owner, object := schemaOf(name), objectOf(name)
		s.types[owner+"."+object] = userType{schema: owner, name: pascal(object), kind: kind, base: base}
	}

	for _, value := range schema.GetEnums() {
		add(value.GetName(), ir.TypeKind_TYPE_KIND_ENUM, nil)
	}

	for _, value := range schema.GetDomains() {
		add(value.GetName(), ir.TypeKind_TYPE_KIND_DOMAIN, value.GetBaseType())
	}

	for _, value := range schema.GetComposites() {
		add(value.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, nil)

		key := schemaOf(value.GetName()) + "." + objectOf(value.GetName())
		found := s.types[key]

		for _, field := range value.GetFields() {
			found.fields = append(found.fields, field.GetType())
		}

		s.types[key] = found
	}

	for _, value := range schema.GetRanges() {
		add(value.GetName(), ir.TypeKind_TYPE_KIND_RANGE, value.GetSubtype())
	}
}

// annotate records the annotations of the go namespace; an unknown name or a
// wrong target is an error at the annotation.
func (s *state) annotate(annotation *ir.AnnotationValue) error {
	switch annotation.GetName() {
	case "type":
		target := annotation.GetObject()
		if target.GetKind() != ir.ObjectKind_OBJECT_KIND_COLUMN || len(annotation.GetArgs()) != 1 ||
			annotation.GetArgs()[0].GetKey() != "" {
			return &plugin.Error{
				Code: codeAnnotation, Message: "@go.type takes one Go type and targets a column", Source: annotation.GetSource(),
			}
		}

		s.columns[strings.Join(target.GetName().GetParts(), ".")] = annotation.GetArgs()[0].GetValue()

		return nil
	case "package":
		if annotation.GetQueryName() == "" || len(annotation.GetArgs()) != 1 {
			return &plugin.Error{
				Code: codeAnnotation, Message: "@go.package takes one package and targets a query",
				Source: annotation.GetSource(),
			}
		}

		s.packages[annotation.GetQueryName()] = annotation.GetArgs()[0].GetValue()

		return nil
	case "row":
		if annotation.GetQueryName() == "" || len(annotation.GetArgs()) != 1 {
			return &plugin.Error{
				Code: codeAnnotation, Message: "@go.row takes one Go type and targets a query",
				Source: annotation.GetSource(),
			}
		}

		return nil
	default:
		return &plugin.Error{
			Code: codeAnnotation, Message: "unknown annotation @go." + annotation.GetName(),
			Source: annotation.GetSource(),
		}
	}
}

func (s *state) importPath(schema string) string {
	return s.options.ImportPath + "/" + packageName(schema)
}

// goType maps a type used from package schema. A user type of another schema
// is qualified with its package.
func (s *state) goType(typ *ir.TypeRef, schema string) (goType, error) {
	if typ.GetElement() != nil {
		element, err := s.goType(typ.GetElement(), schema)
		if err != nil {
			return goType{}, err
		}

		return goType{
			expr:    strings.Repeat("[]", max(1, int(typ.GetArrayDimensions()))) + element.expr,
			imports: element.imports,
		}, nil
	}

	key := typ.GetPgName()

	if isUserType(typ) {
		key = schemaOf(typ.GetUdt().GetName()) + "." + objectOf(typ.GetUdt().GetName())
	}

	if configured, ok := s.options.Types[key]; ok {
		return override(configured), nil
	}

	if !isUserType(typ) {
		if mapped, ok := scalar(typ.GetPgName()); ok {
			return mapped, nil
		}

		return goType{}, &plugin.Error{Code: "RTL-G04", Message: "no Go type for PostgreSQL type " + key +
			"; set options.types or @go.type"}
	}

	return s.userGoType(key, schema)
}

func (s *state) userGoType(key, schema string) (goType, error) {
	found, ok := s.types[key]
	if !ok {
		return goType{}, &plugin.Error{Code: "RTL-G04", Message: "undeclared type " + key}
	}

	if found.kind == ir.TypeKind_TYPE_KIND_RANGE {
		element, err := s.goType(found.base, schema)
		if err != nil {
			return goType{}, err
		}

		return rangeOf(element, false), nil
	}

	if found.schema == schema {
		return goType{expr: found.name}, nil
	}

	return goType{expr: packageName(found.schema) + "." + found.name, imports: []string{s.importPath(found.schema)}}, nil
}

// columnType is a column's Go type as named from package schema, honoring
// @go.type.
func (s *state) columnType(table *ir.QualifiedName, column *ir.Column, schema string) (goType, error) {
	key := strings.Join(append(slices.Clone(table.GetParts()), column.GetName()), ".")
	if configured, ok := s.columns[key]; ok {
		return override(configured), nil
	}

	return s.goType(column.GetType(), schema)
}
