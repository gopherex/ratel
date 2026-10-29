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
	ordered := proto.CloneOf(schema)
	slices.SortFunc(ordered.GetFunctions(), func(left, right *ir.Function) int {
		return cmp.Compare(strings.Join(left.GetName().GetParts(), "\x00"), strings.Join(right.GetName().GetParts(), "\x00"))
	})
	slices.SortFunc(ordered.GetBuiltinTypes(), func(left, right *ir.BuiltinType) int {
		return cmp.Compare(left.GetType().GetPgName(), right.GetType().GetPgName())
	})
	slices.SortFunc(ordered.GetBuiltinOperators(), func(left, right *ir.BuiltinOperator) int {
		return cmp.Compare(operatorKey(left), operatorKey(right))
	})
	slices.SortFunc(ordered.GetBuiltinCasts(), func(left, right *ir.BuiltinCast) int {
		return cmp.Compare(castKey(left), castKey(right))
	})

	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(ordered)
	if err != nil {
		return nil, fmt.Errorf("encode builtin snapshot: %w", err)
	}

	return encoded, nil
}

func operatorKey(operator *ir.BuiltinOperator) string {
	return operator.GetName() + "\x00" + operator.GetLeft().GetPgName() + "\x00" + operator.GetRight().GetPgName()
}

func castKey(cast *ir.BuiltinCast) string {
	return cast.GetSource().GetPgName() + "\x00" + cast.GetTarget().GetPgName()
}
