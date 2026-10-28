package ratel

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// ValueTypeError reports a PostgreSQL type missing from the connection's map.
type ValueTypeError struct {
	Name string
}

func (err ValueTypeError) Error() string {
	return fmt.Sprintf("ratel: PostgreSQL type %q is not registered", err.Name)
}

// RegisterValue enables Null[T], Opt[T], and Opt[Null[T]] for a PostgreSQL type.
// Call it during connection setup, after registering the underlying codec and
// before using the map. It preserves that codec, including custom JSON handling.
// The name also supplies the default type for queries without parameter OIDs.
func RegisterValue[T any](typeMap *pgtype.Map, name string) error {
	dataType, found := typeMap.TypeForName(name)
	if !found {
		return ValueTypeError{Name: name}
	}

	var codec pgtype.Codec

	switch wrapped := dataType.Codec.(type) {
	case *valueCodec:
		codec = wrapped.Codec
	default:
		codec = wrapped
	}

	wrapped := *dataType
	wrapped.Codec = &valueCodec{Codec: codec, typeMap: typeMap, scans: make(map[valuePlanKey]*valuePlan)}
	typeMap.RegisterType(&wrapped)

	for _, value := range []any{
		Null[T]{}, new(Null[T]), Opt[T]{}, new(Opt[T]), Opt[Null[T]]{}, new(Opt[Null[T]]),
	} {
		typeMap.RegisterDefaultPgType(value, name)
	}

	return nil
}

type valueEncoder interface {
	encodeValue(plan *valuePlan, buffer []byte) ([]byte, error)
	encodePlan(typeMap *pgtype.Map, oid uint32, format int16) pgtype.EncodePlan
	encodeType() any
}

type valueScanner interface {
	scanValue(plan pgtype.ScanPlan, source []byte) error
	scanTarget() any
	valueType() any
}

type valueType[T any] struct{}

type valuePlanKey struct {
	typeID any
	oid    uint32
	format int16
}

// The codec must intercept optionals before JSON's catch-all plans. Map's
// TryWrap hooks run after codecs, so they cannot handle arbitrary JSON targets.
type valueCodec struct {
	pgtype.Codec
	typeMap *pgtype.Map
	scans   map[valuePlanKey]*valuePlan
}

func (codec *valueCodec) PlanEncode(typeMap *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	if !codec.FormatSupported(format) {
		return nil
	}

	switch value := value.(type) {
	case valueEncoder:
		return &valuePlan{
			typeMap: typeMap, oid: oid, format: format,
			encode: value.encodePlan(typeMap, oid, format), typeID: value.encodeType(),
		}
	default:
		return codec.Codec.PlanEncode(typeMap, oid, format, value)
	}
}

func (codec *valueCodec) PlanScan(typeMap *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	if !codec.FormatSupported(format) {
		return nil
	}

	switch target := target.(type) {
	case valueScanner:
		key := valuePlanKey{typeID: target.valueType(), oid: oid, format: format}
		if typeMap == codec.typeMap {
			if plan := codec.scans[key]; plan != nil {
				return plan
			}
		}

		plan := &valuePlan{scan: typeMap.PlanScan(oid, format, target.scanTarget()), typeID: key.typeID}
		// Map.Copy shares codecs, but delegate plans belong to the requesting map.
		// Only the owning map writes this cache, under pgx's map ownership rules.
		if typeMap == codec.typeMap {
			codec.scans[key] = plan
		}

		return plan
	default:
		return codec.Codec.PlanScan(typeMap, oid, format, target)
	}
}

type valuePlan struct {
	typeMap *pgtype.Map
	oid     uint32
	format  int16
	encode  pgtype.EncodePlan
	scan    pgtype.ScanPlan
	typeID  any
}

func (plan *valuePlan) Encode(value any, buffer []byte) ([]byte, error) {
	switch value := value.(type) {
	case valueEncoder:
		if value.encodeType() != plan.typeID {
			return nil, fmt.Errorf("encode optional: %w", pgtype.ErrScanTargetTypeChanged)
		}

		return value.encodeValue(plan, buffer)
	default:
		return nil, fmt.Errorf("encode optional: %w", pgtype.ErrScanTargetTypeChanged)
	}
}

func (plan *valuePlan) Scan(source []byte, target any) error {
	switch target := target.(type) {
	case valueScanner:
		if target.valueType() != plan.typeID {
			return fmt.Errorf("scan optional: %w", pgtype.ErrScanTargetTypeChanged)
		}

		return target.scanValue(plan.scan, source)
	default:
		return fmt.Errorf("scan optional: %w", pgtype.ErrScanTargetTypeChanged)
	}
}
