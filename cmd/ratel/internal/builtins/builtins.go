// Package builtins provides generated, version-specific signatures of
// pg_catalog and of the contrib extensions shipped with PostgreSQL.
package builtins

import (
	"embed"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"
)

//go:generate go run ./gen -out data

//go:embed data/*.binpb
var snapshots embed.FS

// UnsupportedMajorError is an expected refusal for an unsupported target server.
type UnsupportedMajorError struct{ Major int }

func (e *UnsupportedMajorError) Error() string {
	return "unsupported PostgreSQL major " + strconv.Itoa(e.Major)
}

// For returns a fresh snapshot owned by the caller; no shared protobuf state escapes.
func For(major int) ([]*ir.Schema, error) {
	const minMajor, maxMajor = 16, 18

	if major < minMajor || major > maxMajor {
		return nil, &UnsupportedMajorError{Major: major}
	}

	data, err := snapshots.ReadFile("data/" + strconv.Itoa(major) + ".binpb")
	if err != nil {
		return nil, fmt.Errorf("read builtin snapshot: %w", err)
	}

	schema := &ir.Schema{}
	if err := proto.Unmarshal(data, schema); err != nil {
		return nil, fmt.Errorf("decode builtin snapshot: %w", err)
	}

	return []*ir.Schema{schema}, nil
}

// ExtensionSchema is the schema the generator installs an extension into;
// the names in an extension snapshot carry it until Extensions renames them.
const ExtensionSchema = "ratel_ext"

// Extensions returns the signatures of the given extensions, each in the
// schema it is created in. An extension the snapshot does not know, such as
// one outside contrib, contributes none.
func Extensions(major int, extensions []*ir.Extension) ([]*ir.Schema, error) {
	if len(extensions) == 0 {
		return nil, nil
	}

	data, err := snapshots.ReadFile("data/" + strconv.Itoa(major) + "-extensions.binpb")
	if err != nil {
		return nil, fmt.Errorf("read extension snapshot: %w", err)
	}

	known := &ir.Catalog{}
	if err := proto.Unmarshal(data, known); err != nil {
		return nil, fmt.Errorf("decode extension snapshot: %w", err)
	}

	var result []*ir.Schema

	for _, extension := range extensions {
		for _, schema := range known.GetSchemas() {
			if schema.GetName() == extension.GetName() {
				result = append(result, relocate(schema, extension.GetSchema()))
			}
		}
	}

	return result, nil
}

// relocate names schema's functions in target, the schema the extension is
// created in. Types and operators are looked up by name alone.
func relocate(schema *ir.Schema, target string) *ir.Schema {
	result := proto.CloneOf(schema)
	result.Name = target

	rename := func(name *ir.QualifiedName) {
		if parts := name.GetParts(); len(parts) > 0 && parts[0] == ExtensionSchema {
			parts[0] = target
		}
	}

	for _, function := range result.GetFunctions() {
		rename(function.GetName())
	}

	for _, operator := range result.GetBuiltinOperators() {
		rename(operator.GetFunction())
	}

	for _, cast := range result.GetBuiltinCasts() {
		rename(cast.GetFunction())
	}

	return result
}
