// Package snapshot serializes generated builtin signatures in canonical order.
package snapshot

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"
)

// Encode leaves its input untouched and sorts catalog sets, never argument lists.
func Encode(schema *ir.Schema) ([]byte, error) {
	return marshal(ordered(schema))
}

// EncodeExtensions writes the signatures of contrib extensions, one schema
// per extension named after it, as the schemas of a Catalog sorted by name.
func EncodeExtensions(extensions []*ir.Schema) ([]byte, error) {
	container := &ir.Catalog{Schemas: make([]*ir.Schema, len(extensions))}
	for index, extension := range extensions {
		container.Schemas[index] = ordered(extension)
	}

	slices.SortFunc(container.GetSchemas(), func(left, right *ir.Schema) int {
		return cmp.Compare(left.GetName(), right.GetName())
	})

	return marshal(container)
}

func marshal(message proto.Message) ([]byte, error) {
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode builtin snapshot: %w", err)
	}

	return encoded, nil
}

// ordered returns a copy of schema with its catalog sets sorted.
func ordered(schema *ir.Schema) *ir.Schema {
	result := proto.CloneOf(schema)
	slices.SortFunc(result.GetFunctions(), func(left, right *ir.Function) int {
		return cmp.Compare(strings.Join(left.GetName().GetParts(), "\x00"), strings.Join(right.GetName().GetParts(), "\x00"))
	})
	slices.SortFunc(result.GetBuiltinTypes(), func(left, right *ir.BuiltinType) int {
		return cmp.Compare(left.GetType().GetPgName(), right.GetType().GetPgName())
	})
	slices.SortFunc(result.GetBuiltinOperators(), func(left, right *ir.BuiltinOperator) int {
		return cmp.Compare(operatorKey(left), operatorKey(right))
	})
	slices.SortFunc(result.GetBuiltinCasts(), func(left, right *ir.BuiltinCast) int {
		return cmp.Compare(castKey(left), castKey(right))
	})

	return result
}

func operatorKey(operator *ir.BuiltinOperator) string {
	return operator.GetName() + "\x00" + operator.GetLeft().GetPgName() + "\x00" + operator.GetRight().GetPgName()
}

func castKey(cast *ir.BuiltinCast) string {
	return cast.GetSource().GetPgName() + "\x00" + cast.GetTarget().GetPgName()
}
