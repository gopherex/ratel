package ratel_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gopherex/ratel"
)

func TestRegisterValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		fail bool
	}{
		{"int8", false},
		{"missing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()

			for range 2 {
				var missing ratel.ValueTypeError

				err := ratel.RegisterValue[int64](typeMap, tc.name)
				if errors.As(err, &missing) != tc.fail {
					t.Fatalf("registration: %v", err)
				}

				if tc.fail {
					if missing.Name != tc.name || !strings.Contains(missing.Error(), tc.name) {
						t.Fatalf("missing type: %+v", missing)
					}

					continue
				}

				for _, value := range []any{
					ratel.NullFrom(int64(42)), ratel.OptFrom(int64(42)), ratel.OptFrom(ratel.NullFrom(int64(42))),
				} {
					encoded, encodeErr := typeMap.Encode(0, pgtype.TextFormatCode, value, nil)
					if encodeErr != nil || string(encoded) != "42" {
						t.Fatalf("default type for %T: %q: %v", value, encoded, encodeErr)
					}
				}
			}
		})
	}
}

func TestRegisterValuePreservesCodec(t *testing.T) {
	t.Parallel()

	typeMap := pgtype.NewMap()
	typeMap.RegisterType(&pgtype.Type{
		Name: "jsonb", OID: pgtype.JSONBOID,
		Codec: &pgtype.JSONBCodec{
			Marshal: func(value any) ([]byte, error) { return json.Marshal([]any{value}) },
			Unmarshal: func(source []byte, target any) error {
				var elements []json.RawMessage
				if err := json.Unmarshal(source, &elements); err != nil {
					return err
				}

				return json.Unmarshal(elements[0], target)
			},
		},
	})

	if err := ratel.RegisterValue[jsonValue](typeMap, "jsonb"); err != nil {
		t.Fatal(err)
	}

	if err := ratel.RegisterValue[string](typeMap, "jsonb"); err != nil {
		t.Fatal(err)
	}

	value := jsonValue{Name: "hello"}

	encoded, err := typeMap.Encode(pgtype.JSONBOID, pgtype.TextFormatCode, ratel.NullFrom(value), nil)
	if err != nil || string(encoded) != `[{"name":"hello"}]` {
		t.Fatalf("custom marshal: %s: %v", encoded, err)
	}

	var scanned ratel.Null[jsonValue]
	if err := typeMap.Scan(pgtype.JSONBOID, pgtype.TextFormatCode, encoded, &scanned); err != nil {
		t.Fatal(err)
	}

	if !scanned.Valid || scanned.V != value {
		t.Fatalf("custom unmarshal: %+v", scanned)
	}

	dataType, found := typeMap.TypeForName("jsonb")
	if !found || dataType.Codec.PreferredFormat() != pgtype.TextFormatCode {
		t.Fatal("changed preferred format")
	}

	decoded, err := dataType.Codec.DecodeValue(typeMap, pgtype.JSONBOID, pgtype.TextFormatCode, encoded)
	if err != nil || decoded == nil {
		t.Fatalf("decode: %v: %v", decoded, err)
	}
}

func TestValuePlanReuse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		initial ratel.Null[int64]
	}{
		{"value", ratel.NullFrom(int64(42))},
		{"null", ratel.Null[int64]{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()
			if err := ratel.RegisterValue[int64](typeMap, "int8"); err != nil {
				t.Fatal(err)
			}

			plan := typeMap.PlanEncode(pgtype.Int8OID, pgtype.TextFormatCode, tc.initial)

			for _, sample := range []struct {
				value ratel.Null[int64]
				want  []byte
			}{
				{ratel.NullFrom(int64(42)), []byte("42")},
				{ratel.Null[int64]{}, nil},
				{ratel.NullFrom(int64(0)), []byte("0")},
			} {
				encoded, err := plan.Encode(sample.value, nil)
				if err != nil || !bytes.Equal(encoded, sample.want) || (encoded == nil) != (sample.want == nil) {
					t.Fatalf("reused plan: %q: %v", encoded, err)
				}
			}
		})
	}
}

func TestValueCodecPlanContracts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		format int16
	}{
		{"text", pgtype.TextFormatCode},
		{"binary", pgtype.BinaryFormatCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()
			if err := ratel.RegisterValue[int64](typeMap, "int8"); err != nil {
				t.Fatal(err)
			}

			dataType, found := typeMap.TypeForName("int8")
			if !found {
				t.Fatal("missing int8")
			}

			const invalidFormat = -1

			if dataType.Codec.PlanEncode(typeMap, dataType.OID, invalidFormat, ratel.Null[int64]{}) != nil {
				t.Fatal("accepted unsupported encode format")
			}

			if dataType.Codec.PlanScan(typeMap, dataType.OID, invalidFormat, new(ratel.Null[int64])) != nil {
				t.Fatal("accepted unsupported scan format")
			}

			encodePlan := typeMap.PlanEncode(dataType.OID, tc.format, ratel.Null[int64]{})
			if _, err := encodePlan.Encode("wrong type", nil); !errors.Is(err, pgtype.ErrScanTargetTypeChanged) {
				t.Fatalf("encode plan misuse: %v", err)
			}

			scanPlan := typeMap.PlanScan(dataType.OID, tc.format, new(ratel.Null[int64]))
			if err := scanPlan.Scan(nil, "wrong type"); !errors.Is(err, pgtype.ErrScanTargetTypeChanged) {
				t.Fatalf("scan plan misuse: %v", err)
			}
		})
	}
}

func BenchmarkRegisterValue(b *testing.B) {
	for b.Loop() {
		typeMap := pgtype.NewMap()
		if err := ratel.RegisterValue[int64](typeMap, "int8"); err != nil {
			b.Fatal(err)
		}
	}
}
