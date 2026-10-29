package ratel

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// UnsetOptError reports an attempt to encode a column that must be omitted.
// Test it with errors.As; unset is not SQL NULL.
type UnsetOptError struct{}

func (UnsetOptError) Error() string {
	return "ratel: cannot encode unset Opt; omit the column"
}

// Null is a nullable value.
type Null[T any] struct {
	V     T
	Valid bool
}

// NullFrom constructs a non-null value.
func NullFrom[T any](value T) Null[T] {
	return Null[T]{V: value, Valid: true}
}

// NullFromPtr constructs a nullable value from a pointer.
func NullFromPtr[T any](value *T) Null[T] {
	if value == nil {
		return Null[T]{}
	}

	return NullFrom(*value)
}

// Ptr returns a pointer to a copy of the value or nil when null.
func (value Null[T]) Ptr() *T {
	if !value.Valid {
		return nil
	}

	return &value.V
}

// IsZero reports whether the value is null.
func (value Null[T]) IsZero() bool {
	return !value.Valid
}

// Arg is the value as a pgx argument: nil for NULL, V otherwise. It needs no
// type registration, unlike passing the wrapper itself.
func (value Null[T]) Arg() any {
	if !value.Valid {
		return nil
	}

	return value.V
}

func (value Null[T]) encodeValue(plan *valuePlan, buffer []byte) ([]byte, error) {
	if !value.Valid {
		return nil, nil
	}

	encoded, err := encodeValue(plan, value.V, buffer)
	if err != nil {
		return nil, fmt.Errorf("encode Null: %w", err)
	}

	return encoded, nil
}

func (value *Null[T]) scanValue(plan pgtype.ScanPlan, source []byte) error {
	*value = Null[T]{}

	if source == nil {
		return nil
	}

	if err := plan.Scan(source, &value.V); err != nil {
		*value = Null[T]{}

		return fmt.Errorf("scan Null: %w", err)
	}

	value.Valid = true

	return nil
}

// Opt is an optional value.
type Opt[T any] struct {
	V   T
	Set bool
}

// OptFrom constructs a set optional value.
func OptFrom[T any](value T) Opt[T] {
	return Opt[T]{V: value, Set: true}
}

// IsZero reports whether the value is unset.
func (value Opt[T]) IsZero() bool {
	return !value.Set
}

func (value Opt[T]) encodeValue(plan *valuePlan, buffer []byte) ([]byte, error) {
	if !value.Set {
		return nil, UnsetOptError{}
	}

	encoded, err := encodeValue(plan, value.V, buffer)
	if err != nil {
		return nil, fmt.Errorf("encode Opt: %w", err)
	}

	return encoded, nil
}

func (value *Opt[T]) scanValue(plan pgtype.ScanPlan, source []byte) error {
	*value = Opt[T]{}

	if err := plan.Scan(source, &value.V); err != nil {
		*value = Opt[T]{}

		return fmt.Errorf("scan Opt: %w", err)
	}

	value.Set = true

	return nil
}

func (*Null[T]) scanTarget() any { return new(T) }

func (*Opt[T]) scanTarget() any { return new(T) }

func (Null[T]) encodeType() any { return valueType[T]{} }

func (Opt[T]) encodeType() any { return valueType[T]{} }

func (*Null[T]) valueType() any { return valueType[T]{} }

func (*Opt[T]) valueType() any { return valueType[T]{} }

func (Null[T]) encodePlan(typeMap *pgtype.Map, oid uint32, format int16) pgtype.EncodePlan {
	return scalarEncodePlan[T](typeMap, oid, format)
}

func (Opt[T]) encodePlan(typeMap *pgtype.Map, oid uint32, format int16) pgtype.EncodePlan {
	return scalarEncodePlan[T](typeMap, oid, format)
}

func scalarEncodePlan[T any](typeMap *pgtype.Map, oid uint32, format int16) pgtype.EncodePlan {
	// Only concrete, non-nullable types bypass Map.Encode's nil-value handling.
	// Interfaces and nil-capable types retain pgx's dynamic planning and NULL semantics.
	switch any((*T)(nil)).(type) {
	case *bool, *int8, *int16, *int32, *int64, *int, *uint8, *uint16, *uint32, *uint64, *uint,
		*float32, *float64, *string:
		var value T

		return typeMap.PlanEncode(oid, format, value)
	default:
		return nil
	}
}

func encodeValue[T any](plan *valuePlan, value T, buffer []byte) ([]byte, error) {
	var encoded []byte

	var err error

	if plan.encode != nil {
		encoded, err = plan.encode.Encode(value, buffer)
	} else {
		encoded, err = plan.typeMap.Encode(plan.oid, plan.format, value, buffer)
	}

	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}

	return encoded, nil
}
