package gengo

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/pkg/ir"
	"github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"
)

// codeCollision is the diagnostic code of two generated Go identifiers that
// are the same.
const codeCollision = "RTL-G05"

// unit accumulates a generated file's body and imports.
type unit struct {
	schema   string // PostgreSQL schema of the package
	body     strings.Builder
	imports  []string
	names    map[string]string // generated identifier -> its source, for collisions
	registry *registry
}

func (u *unit) printf(format string, args ...any) { fmt.Fprintf(&u.body, format, args...) }

func (u *unit) use(t goType) string {
	u.imports = append(u.imports, t.imports...)

	return t.expr
}

// declare reserves a package-level identifier; a second source for the same
// identifier is a generation error naming both.
func (u *unit) declare(identifier, source string) error {
	if previous, exists := u.names[identifier]; exists {
		return &plugin.Error{Code: codeCollision, Message: "Go identifier " + identifier + " of " + source +
			" collides with " + previous}
	}

	u.names[identifier] = source

	return nil
}

// schemaFiles renders a schema's package; an empty schema has none.
func (s *state) schemaFiles(schema *ir.Schema) ([]*pluginpb.File, error) {
	if len(schema.GetTables())+len(schema.GetViews())+len(schema.GetMaterializedViews())+
		len(schema.GetEnums())+len(schema.GetDomains())+len(schema.GetComposites()) == 0 {
		return nil, nil
	}

	file := &unit{schema: schema.GetName(), names: make(map[string]string), registry: newRegistry()}

	steps := []func(*unit, *ir.Schema) error{s.enums, s.domains, s.composites, s.tables, s.views}
	for _, step := range steps {
		if err := step(file, schema); err != nil {
			return nil, err
		}
	}

	if !file.registry.empty() {
		if err := file.declare("RegisterTypes", "schema "+schema.GetName()); err != nil {
			return nil, err
		}

		writeRegisterTypes(file)
	}

	name := packageName(schema.GetName())

	contents, err := source(name, file.imports, file.body.String())
	if err != nil {
		return nil, err
	}

	return []*pluginpb.File{{Path: name + "/" + name + ".go", Contents: contents}}, nil
}

func (s *state) enums(file *unit, schema *ir.Schema) error {
	for _, enum := range schema.GetEnums() {
		if s.protoOnlyEnum(enum.GetName()) {
			continue
		}

		object := objectOf(enum.GetName())
		typeName := pascal(object)

		if err := file.declare(typeName, "enum "+object); err != nil {
			return err
		}

		file.printf("// %s is the PostgreSQL enum %s.%s.\ntype %s string\n\nconst (\n",
			typeName, schema.GetName(), object, typeName)

		for _, label := range enum.GetLabels() {
			constant := typeName + enumLabel(label)
			if err := file.declare(constant, "enum "+object+" label "+label); err != nil {
				return err
			}

			file.printf("\t%s %s = %s\n", constant, typeName, strconv.Quote(label))
		}

		file.printf(")\n\n")
	}

	return nil
}

// enumLabel turns an enum label into an identifier suffix.
func enumLabel(label string) string {
	name := pascal(strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == ' ' || r == '.' {
			return '_'
		}

		return r
	}, label))
	if name == "" {
		return "Empty"
	}

	return name
}

func (s *state) domains(file *unit, schema *ir.Schema) error {
	for _, domain := range schema.GetDomains() {
		object := objectOf(domain.GetName())
		typeName := pascal(object)

		if err := file.declare(typeName, "domain "+object); err != nil {
			return err
		}

		base, err := s.goType(domain.GetBaseType(), schema.GetName())
		if err != nil {
			return err
		}

		// A domain is its base type to Go; PostgreSQL enforces its constraints.
		file.printf("// %s is the PostgreSQL domain %s.%s.\ntype %s = %s\n\n",
			typeName, schema.GetName(), object, typeName, file.use(base))
	}

	return nil
}

func (s *state) composites(file *unit, schema *ir.Schema) error {
	for _, composite := range schema.GetComposites() {
		object := objectOf(composite.GetName())
		typeName := pascal(object)

		if err := file.declare(typeName, "composite "+object); err != nil {
			return err
		}

		file.printf("// %s is the PostgreSQL composite type %s.%s.\ntype %s struct {\n",
			typeName, schema.GetName(), object, typeName)
		s.load(file, &ir.TypeRef{
			Kind: ir.TypeKind_TYPE_KIND_COMPOSITE, PgName: object,
			Udt: &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, Name: composite.GetName()},
		})

		for _, field := range composite.GetFields() {
			fieldType, err := s.goType(field.GetType(), schema.GetName())
			if err != nil {
				return err
			}

			// Composite fields are nullable in PostgreSQL.
			file.imports = append(file.imports, ratelImport)
			// pgx's composite codec scans the fields itself, so their ratel.Null
			// wrappers are registered on the connection.
			s.use(file, field.GetType())
			file.registry.values[valuePair{goType: file.use(fieldType), pgType: s.valueName(field.GetType())}] = true
			file.printf("\t%s ratel.Null[%s] `db:%s`\n", pascal(field.GetName()), file.use(fieldType),
				strconv.Quote(field.GetName()))
		}

		file.printf("}\n\n")
	}

	return nil
}
