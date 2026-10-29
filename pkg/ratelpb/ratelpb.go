// Package ratelpb converts proto message fields to and from the values pgx
// sends and reads. Code that ratel-gen-go generates for proto models calls
// it; the conversions are described in docs/proto-frontend.md (Go models).
//
// Write conversions return values that pgx encodes; a conversion that can
// fail (protojson, a Duration with nanoseconds) fails when pgx encodes it, so
// building a write never returns an error.
package ratelpb

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Errors of conversions from database values.
var (
	// ErrOutOfRange means a database value does not fit the proto field.
	ErrOutOfRange = errors.New("ratelpb: value out of range of the proto field")
	// ErrUnknownEnum means a stored name is not a value of the proto enum.
	ErrUnknownEnum = errors.New("ratelpb: unknown enum value name")
	// ErrMonths means an interval has months, which a Duration cannot hold:
	// a month has no fixed length.
	ErrMonths = errors.New("ratelpb: interval with months is not a Duration")
	// ErrSubMicrosecond means a Duration is finer than PostgreSQL's interval.
	ErrSubMicrosecond = errors.New("ratelpb: Duration finer than a microsecond")
)

const (
	microsecond = int64(time.Microsecond)
	decimalBase = 10 // pgtype.Numeric is Int * 10^Exp
)

// Set is value when ok holds and SQL NULL otherwise: the column of a field
// inside an absent embedded message or an unset oneof member.
func Set[T any](ok bool, value T) any {
	if !ok {
		return nil
	}

	return value
}

// Time is a Timestamp as pgx sends it; nil is SQL NULL.
func Time(value *timestamppb.Timestamp) any {
	if value == nil {
		return nil
	}

	return value.AsTime()
}

// FromTime is the Timestamp of a scanned value; NULL is nil.
func FromTime(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}

	return timestamppb.New(*value)
}

// Interval is a Duration as pgx sends it; nil is SQL NULL. A Duration finer
// than a microsecond fails when pgx encodes it.
func Interval(value *durationpb.Duration) any {
	if value == nil {
		return nil
	}

	return interval{value: value}
}

// interval encodes a Duration through pgtype.IntervalValuer.
type interval struct{ value *durationpb.Duration }

// IntervalValue implements pgtype.IntervalValuer.
func (i interval) IntervalValue() (pgtype.Interval, error) {
	if err := i.value.CheckValid(); err != nil {
		return pgtype.Interval{}, fmt.Errorf("ratelpb: %w", err)
	}

	duration := i.value.AsDuration()
	if duration%time.Microsecond != 0 {
		return pgtype.Interval{}, ErrSubMicrosecond
	}

	return pgtype.Interval{Microseconds: int64(duration) / microsecond, Valid: true}, nil
}

// FromInterval is the Duration of a scanned, non-NULL interval. Days count
// as 24 hours.
func FromInterval(value pgtype.Interval) (*durationpb.Duration, error) {
	if value.Months != 0 {
		return nil, ErrMonths
	}

	day := int64(24 * time.Hour / time.Microsecond)

	return durationpb.New(time.Duration(int64(value.Days)*day+value.Microseconds) * time.Microsecond), nil
}

// jsonValue encodes as JSON when pgx encodes it, so a marshaling failure is
// an encoding error of the statement.
type jsonValue struct{ marshal func() ([]byte, error) }

// MarshalJSON implements json.Marshaler.
func (v jsonValue) MarshalJSON() ([]byte, error) { return v.marshal() }

// JSON is a message as protojson for a jsonb column; nil is SQL NULL.
func JSON(value proto.Message) any {
	if value == nil || !value.ProtoReflect().IsValid() {
		return nil
	}

	return jsonValue{marshal: func() ([]byte, error) { return protojson.Marshal(value) }}
}

// FromJSON is the message of a scanned, non-NULL jsonb value.
func FromJSON[T any, P interface {
	*T
	proto.Message
}](data []byte) (P, error) {
	message := P(new(T))
	if err := protojson.Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("ratelpb: %w", err)
	}

	return message, nil
}

// JSONList is a repeated message as a jsonb array of protojson objects.
func JSONList[P proto.Message](values []P) any {
	return jsonValue{marshal: func() ([]byte, error) {
		raw := make([]json.RawMessage, len(values))
		for index, value := range values {
			encoded, err := protojson.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("ratelpb: %w", err)
			}

			raw[index] = encoded
		}

		return json.Marshal(raw)
	}}
}

// FromJSONList is the repeated message of a scanned jsonb array.
func FromJSONList[T any, P interface {
	*T
	proto.Message
}](data []byte) ([]P, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("ratelpb: %w", err)
	}

	values := make([]P, len(raw))

	for index, element := range raw {
		value, err := FromJSON[T, P](element)
		if err != nil {
			return nil, err
		}

		values[index] = value
	}

	return values, nil
}

// JSONMap is a map with message values as a jsonb object of protojson values.
func JSONMap[K comparable, P proto.Message](values map[K]P) any {
	return jsonValue{marshal: func() ([]byte, error) {
		raw := make(map[K]json.RawMessage, len(values))
		for key, value := range values {
			encoded, err := protojson.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("ratelpb: %w", err)
			}

			raw[key] = encoded
		}

		return json.Marshal(raw)
	}}
}

// FromJSONMap is the map with message values of a scanned jsonb object.
func FromJSONMap[K comparable, T any, P interface {
	*T
	proto.Message
}](data []byte) (map[K]P, error) {
	var raw map[K]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("ratelpb: %w", err)
	}

	values := make(map[K]P, len(raw))

	for key, element := range raw {
		value, err := FromJSON[T, P](element)
		if err != nil {
			return nil, err
		}

		values[key] = value
	}

	return values, nil
}

// ScalarMap is a map with scalar values as a jsonb object; an empty map,
// which proto3 does not tell from an absent one, is {}.
func ScalarMap[K comparable, V any](values map[K]V) any {
	if values == nil {
		values = map[K]V{}
	}

	return jsonValue{marshal: func() ([]byte, error) { return json.Marshal(values) }}
}

// FromScalarMap is the map with scalar values of a scanned jsonb object.
func FromScalarMap[K comparable, V any](data []byte) (map[K]V, error) {
	values := make(map[K]V)
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("ratelpb: %w", err)
	}

	return values, nil
}

// EnumNames are the value names of repeated enum values, as stored.
func EnumNames[E interface{ String() string }](values []E) []string {
	names := make([]string, len(values))

	for index, value := range values {
		names[index] = value.String()
	}

	return names
}

// FromEnum is the enum value of a stored name; values is the enum's
// generated <Enum>_value map.
func FromEnum[E ~int32](values map[string]int32, name string) (E, error) {
	value, ok := values[name]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownEnum, name)
	}

	return E(value), nil
}

// FromEnums is the repeated enum of stored names.
func FromEnums[E ~int32](values map[string]int32, names []string) ([]E, error) {
	enums := make([]E, len(names))

	for index, name := range names {
		value, err := FromEnum[E](values, name)
		if err != nil {
			return nil, err
		}

		enums[index] = value
	}

	return enums, nil
}

// Wrapped is the value of a wrapper message; nil is SQL NULL.
func Wrapped[T any, W interface {
	comparable
	GetValue() T
}](value W) any {
	var absent W
	if value == absent {
		return nil
	}

	return value.GetValue()
}

// Uint32 is the uint32 field of a scanned int8 value.
func Uint32(value int64) (uint32, error) {
	if value < 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("%w: %d", ErrOutOfRange, value)
	}

	return uint32(value), nil
}

// Numeric is a uint64 field as pgx sends a numeric value.
func Numeric(value uint64) pgtype.Numeric {
	return pgtype.Numeric{Int: new(big.Int).SetUint64(value), Valid: true}
}

// Uint64 is the uint64 field of a scanned, non-NULL numeric value.
func Uint64(value pgtype.Numeric) (uint64, error) {
	if value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
		return 0, fmt.Errorf("%w: not a finite number", ErrOutOfRange)
	}

	integer := new(big.Int).Set(value.Int)
	scale := new(big.Int).Exp(big.NewInt(decimalBase), big.NewInt(int64(abs(value.Exp))), nil)

	if value.Exp >= 0 {
		integer.Mul(integer, scale)
	} else {
		remainder := new(big.Int)
		integer.QuoRem(integer, scale, remainder)

		if remainder.Sign() != 0 {
			return 0, fmt.Errorf("%w: not an integer", ErrOutOfRange)
		}
	}

	if !integer.IsUint64() {
		return 0, fmt.Errorf("%w: %s", ErrOutOfRange, integer)
	}

	return integer.Uint64(), nil
}

func abs(value int32) int32 {
	if value < 0 {
		return -value
	}

	return value
}
