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

func (value Null[T]) encodeValue(typeMap *pgtype.Map, oid uint32, format int16, buffer []byte) ([]byte, error) {
	if !value.Valid {
		return nil, nil
	}

	encoded, err := typeMap.Encode(oid, format, value.V, buffer)
	if err != nil {
		return nil, fmt.Errorf("encode Null: %w", err)
	}

	return encoded, nil
}

func (value *Null[T]) scanValue(typeMap *pgtype.Map, oid uint32, format int16, source []byte) error {
	*value = Null[T]{}

	if source == nil {
		return nil
	}

	var decoded T
	if err := typeMap.Scan(oid, format, source, &decoded); err != nil {
		return fmt.Errorf("scan Null: %w", err)
	}

	*value = NullFrom(decoded)

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

func (value Opt[T]) encodeValue(typeMap *pgtype.Map, oid uint32, format int16, buffer []byte) ([]byte, error) {
	if !value.Set {
		return nil, UnsetOptError{}
	}

	encoded, err := typeMap.Encode(oid, format, value.V, buffer)
	if err != nil {
		return nil, fmt.Errorf("encode Opt: %w", err)
	}

	return encoded, nil
}

func (value *Opt[T]) scanValue(typeMap *pgtype.Map, oid uint32, format int16, source []byte) error {
	*value = Opt[T]{}

	var decoded T
	if err := typeMap.Scan(oid, format, source, &decoded); err != nil {
		return fmt.Errorf("scan Opt: %w", err)
	}

	*value = OptFrom(decoded)

	return nil
}
