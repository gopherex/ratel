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

	switch dataType.Codec.(type) {
	case *valueCodec:
	default:
		wrapped := *dataType
		wrapped.Codec = &valueCodec{Codec: dataType.Codec}
		typeMap.RegisterType(&wrapped)
	}

	for _, value := range []any{
		Null[T]{}, new(Null[T]), Opt[T]{}, new(Opt[T]), Opt[Null[T]]{}, new(Opt[Null[T]]),
	} {
		typeMap.RegisterDefaultPgType(value, name)
	}

	return nil
}

type valueEncoder interface {
	encodeValue(typeMap *pgtype.Map, oid uint32, format int16, buffer []byte) ([]byte, error)
}

type valueScanner interface {
	scanValue(typeMap *pgtype.Map, oid uint32, format int16, source []byte) error
}

// The codec must intercept optionals before JSON's catch-all plans. Map's
// TryWrap hooks run after codecs, so they cannot handle arbitrary JSON targets.
type valueCodec struct {
	pgtype.Codec
}

func (codec *valueCodec) PlanEncode(typeMap *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	if !codec.FormatSupported(format) {
		return nil
	}

	switch value.(type) {
	case valueEncoder:
		return &valuePlan{typeMap: typeMap, oid: oid, format: format}
	default:
		return codec.Codec.PlanEncode(typeMap, oid, format, value)
	}
}

func (codec *valueCodec) PlanScan(typeMap *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	if !codec.FormatSupported(format) {
		return nil
	}

	switch target.(type) {
	case valueScanner:
		return &valuePlan{typeMap: typeMap, oid: oid, format: format}
	default:
		return codec.Codec.PlanScan(typeMap, oid, format, target)
	}
}

type valuePlan struct {
	typeMap *pgtype.Map
	oid     uint32
	format  int16
}

func (plan *valuePlan) Encode(value any, buffer []byte) ([]byte, error) {
	switch value := value.(type) {
	case valueEncoder:
		return value.encodeValue(plan.typeMap, plan.oid, plan.format, buffer)
	default:
		return nil, fmt.Errorf("encode optional: %w", pgtype.ErrScanTargetTypeChanged)
	}
}

func (plan *valuePlan) Scan(source []byte, target any) error {
	switch target := target.(type) {
	case valueScanner:
		return target.scanValue(plan.typeMap, plan.oid, plan.format, source)
	default:
		return fmt.Errorf("scan optional: %w", pgtype.ErrScanTargetTypeChanged)
	}
}
