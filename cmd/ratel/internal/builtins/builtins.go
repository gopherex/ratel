// Package builtins provides generated, version-specific pg_catalog signatures.
package builtins

import (
	"embed"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"
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
