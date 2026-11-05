package gengo

import (
	"fmt"
	"slices"
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
	pkg      string // Go package name of a model package; empty for a query package
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

// relationPackage is the package of a table or view: the first packages rule
// matching its name, otherwise its schema's.
func (s *state) relationPackage(name *ir.QualifiedName) string {
	for _, rule := range s.options.Packages {
		if rule.matches(schemaOf(name), objectOf(name)) {
			return rule.Package
		}
	}

	return packageName(schemaOf(name))
}

// units are the generated packages by Go package name; a package with
// nothing in it has no unit and no file.
type units map[string]*unit

func (u units) of(pkg string) *unit {
	if u[pkg] == nil {
		u[pkg] = &unit{pkg: pkg, names: make(map[string]string), registry: newRegistry()}
	}

	return u[pkg]
}

// render writes one file per package, <pkg>/<pkg>.go.
func (s *state) render(all units) ([]*pluginpb.File, error) {
	if err := importCycle(s.options.ImportPath, all); err != nil {
		return nil, err
	}

	files := make([]*pluginpb.File, 0, len(all))

	for _, pkg := range slices.Sorted(mapKeys(all)) {
		file := all[pkg]
		if !file.registry.empty() {
			if err := file.declare("RegisterTypes", "package "+pkg); err != nil {
				return nil, err
			}

			writeRegisterTypes(file)
		}

		contents, err := source(pkg, file.imports, file.body.String())
		if err != nil {
			return nil, err
		}

		files = append(files, &pluginpb.File{Path: pkg + "/" + pkg + ".go", Contents: contents})
	}

	return files, nil
}

// schemaModels writes a schema's enums, domains and composites into the
// package named after it, and each table and view into its relationPackage.
func (s *state) schemaModels(schema *ir.Schema, all units) error {
	if len(schema.GetEnums())+len(schema.GetDomains())+len(schema.GetComposites()) > 0 {
		file := all.of(packageName(schema.GetName()))
		for _, step := range []func(*unit, *ir.Schema) error{s.enums, s.domains, s.composites} {
			if err := step(file, schema); err != nil {
				return err
			}
		}
	}

	for _, table := range schema.GetTables() {
		if table.GetPartitionOf() != nil {
			continue // partitions are reached through their parent
		}

		if err := s.writeTable(all.of(s.relationPackage(table.GetName())), schema.GetName(), table); err != nil {
			return err
		}
	}

	for _, view := range schema.GetViews() {
		if err := s.view(all.of(s.relationPackage(view.GetName())), view.GetName(), view.GetOutput(), "view"); err != nil {
			return err
		}
	}

	for _, view := range schema.GetMaterializedViews() {
		file := all.of(s.relationPackage(view.GetName()))
		if err := s.view(file, view.GetName(), view.GetOutput(), "materialized view"); err != nil {
			return err
		}
	}

	return nil
}

// importCycle refuses model packages that import each other, directly or
// through others: foreign keys between their tables in both directions.
func importCycle(importPath string, units map[string]*unit) error {
	const visiting, done = 1, 2

	state := make(map[string]int, len(units))

	var walk func(pkg string, path []string) []string

	walk = func(pkg string, path []string) []string {
		switch state[pkg] {
		case visiting:
			return append(path[slices.Index(path, pkg):], pkg)
		case done:
			return nil
		}

		state[pkg] = visiting

		path = append(path, pkg)

		for _, imported := range slices.Sorted(slices.Values(units[pkg].imports)) {
			dependency, local := strings.CutPrefix(imported, importPath+"/")
			if !local || units[dependency] == nil || dependency == pkg {
				continue
			}

			if cycle := walk(dependency, path); cycle != nil {
				return cycle
			}
		}

		state[pkg] = done

		return nil
	}

	for _, pkg := range slices.Sorted(mapKeys(units)) {
		if cycle := walk(pkg, nil); cycle != nil {
			return &plugin.Error{Code: "RTL-G11", Message: "generated packages import each other: " +
				strings.Join(cycle, " -> ") + "; their tables reference each other through foreign keys, " +
				"so place them in one package"}
		}
	}

	return nil
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

		base, err := s.goType(domain.GetBaseType(), file.pkg)
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
			fieldType, err := s.goType(field.GetType(), file.pkg)
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
