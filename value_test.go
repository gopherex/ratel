package ratel_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gopherex/ratel"
)

func TestNull(t *testing.T) {
	t.Parallel()

	value := 42

	for _, tc := range []struct {
		name  string
		value ratel.Null[int]
		want  int
		valid bool
	}{
		{"zero", ratel.Null[int]{}, 0, false},
		{"value", ratel.NullFrom(42), 42, true},
		{"pointer", ratel.NullFromPtr(&value), 42, true},
		{"nil", ratel.NullFromPtr[int](nil), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.value.V != tc.want || tc.value.Valid != tc.valid || tc.value.IsZero() == tc.valid {
				t.Fatalf("value = %+v", tc.value)
			}

			ptr := tc.value.Ptr()
			if (ptr != nil) != tc.valid || (ptr != nil && *ptr != tc.want) {
				t.Fatal("unexpected pointer")
			}

			if arg := tc.value.Arg(); (arg != nil) != tc.valid || (arg != nil && arg != any(tc.want)) {
				t.Fatalf("Arg() = %#v", arg)
			}
		})
	}
}

func TestOpt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value ratel.Opt[int]
		want  int
		set   bool
	}{
		{"zero", ratel.Opt[int]{}, 0, false},
		{"set zero", ratel.OptFrom(0), 0, true},
		{"set", ratel.OptFrom(42), 42, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.value.V != tc.want || tc.value.Set != tc.set || tc.value.IsZero() == tc.set {
				t.Fatalf("value = %+v", tc.value)
			}
		})
	}
}

type jsonValue struct {
	Name string `json:"name"`
}

func TestValueCodecs(t *testing.T) {
	t.Parallel()

	value := int64(42)

	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"int8", valueCodecCase("int8", value)},
		{"text", valueCodecCase("text", "hello")},
		{"timestamptz", valueCodecCase("timestamptz", time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))},
		{"json", valueCodecCase("json", jsonValue{Name: "hello"})},
		{"jsonb", valueCodecCase("jsonb", jsonValue{Name: "hello"})},
		{"array", valueCodecCase("_int8", []int64{1, 2})},
		{"pointer", valueCodecCase("int8", &value)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func valueCodecCase[T any](name string, value T) func(*testing.T) {
	return func(t *testing.T) {
		t.Helper()
		checkValueCodec(t, name, value)
	}
}

func checkValueCodec[T any](t *testing.T, name string, value T) {
	t.Helper()

	for _, format := range []int16{pgtype.TextFormatCode, pgtype.BinaryFormatCode} {
		typeMap := pgtype.NewMap()
		if err := ratel.RegisterValue[T](typeMap, name); err != nil {
			t.Fatal(err)
		}

		dataType, found := typeMap.TypeForName(name)
		if !found {
			t.Fatal("missing type", name)
		}

		want, err := typeMap.Encode(dataType.OID, format, value, nil)
		if err != nil {
			t.Fatal(err)
		}

		nullable := ratel.NullFrom(value)
		optional := ratel.OptFrom(value)

		for _, input := range []any{nullable, &nullable, optional, &optional, ratel.OptFrom(nullable)} {
			encoded, encodeErr := typeMap.Encode(dataType.OID, format, input, nil)
			if encodeErr != nil || !bytes.Equal(encoded, want) {
				t.Fatalf("format %d, %T: encoded %x, want %x: %v", format, input, encoded, want, encodeErr)
			}
		}

		checkValueScan[T](t, typeMap, dataType.OID, format, want)

		encoded, err := typeMap.Encode(dataType.OID, format, ratel.Null[T]{V: value}, []byte{1})
		if err != nil || encoded != nil {
			t.Fatalf("encode NULL: %x: %v", encoded, err)
		}

		var unset ratel.UnsetOptError

		_, err = typeMap.Encode(dataType.OID, format, ratel.Opt[T]{}, nil)
		if !errors.As(err, &unset) {
			t.Fatalf("unset error: %v", err)
		}
	}
}

func checkValueScan[T any](t *testing.T, typeMap *pgtype.Map, oid uint32, format int16, source []byte) {
	t.Helper()

	var expected T
	if err := typeMap.Scan(oid, format, source, &expected); err != nil {
		t.Fatal(err)
	}

	var nullable ratel.Null[T]
	if err := typeMap.Scan(oid, format, source, &nullable); err != nil {
		t.Fatal(err)
	}

	if !nullable.Valid || !reflect.DeepEqual(nullable.V, expected) {
		t.Fatalf("scan: %+v, want %v", nullable, expected)
	}

	if err := typeMap.Scan(oid, format, nil, &nullable); err != nil || nullable.Valid {
		t.Fatalf("scan NULL: %+v: %v", nullable, err)
	}

	var zero T
	if !reflect.DeepEqual(nullable.V, zero) {
		t.Fatalf("NULL retained stale value: %v", nullable.V)
	}

	var optional ratel.Opt[ratel.Null[T]]
	if err := typeMap.Scan(oid, format, source, &optional); err != nil || !optional.Set || !optional.V.Valid {
		t.Fatalf("scan optional: %+v: %v", optional, err)
	}

	if err := typeMap.Scan(oid, format, nil, &optional); err != nil || !optional.Set || optional.V.Valid {
		t.Fatalf("scan optional NULL: %+v: %v", optional, err)
	}
}

func TestValueScanFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{"invalid", []byte("not-an-integer")},
		{"overflow", []byte("9223372036854775808")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()
			if err := ratel.RegisterValue[int64](typeMap, "int8"); err != nil {
				t.Fatal(err)
			}

			nullable := ratel.NullFrom(int64(42))
			optional := ratel.OptFrom(int64(42))

			for _, target := range []any{&nullable, &optional} {
				if err := typeMap.Scan(pgtype.Int8OID, pgtype.TextFormatCode, tc.src, target); err == nil {
					t.Fatal("expected scan error")
				}
			}

			if nullable.Valid || nullable.V != 0 || optional.Set || optional.V != 0 {
				t.Fatalf("failed scan retained state: %+v %+v", nullable, optional)
			}
		})
	}
}

func TestValueEncodeFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value any
	}{
		{"null", ratel.NullFrom("invalid")},
		{"opt", ratel.OptFrom("invalid")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()
			if err := ratel.RegisterValue[string](typeMap, "int8"); err != nil {
				t.Fatal(err)
			}

			if _, err := typeMap.Encode(pgtype.Int8OID, pgtype.BinaryFormatCode, tc.value, nil); err == nil {
				t.Fatal("expected encoding error")
			}
		})
	}
}

func TestValueNilPointers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value any
	}{
		{"nil null pointer", (*ratel.Null[int64])(nil)},
		{"nil opt pointer", (*ratel.Opt[int64])(nil)},
		{"null with nil value", ratel.NullFrom[*int64](nil)},
		{"opt with nil value", ratel.OptFrom[*int64](nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()
			if err := ratel.RegisterValue[*int64](typeMap, "int8"); err != nil {
				t.Fatal(err)
			}

			encoded, err := typeMap.Encode(pgtype.Int8OID, pgtype.BinaryFormatCode, tc.value, nil)
			if err != nil || encoded != nil {
				t.Fatalf("encode nil pointer: %x: %v", encoded, err)
			}
		})
	}
}

func TestValueJSONNull(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"json", "jsonb"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typeMap := pgtype.NewMap()
			if err := ratel.RegisterValue[*jsonValue](typeMap, name); err != nil {
				t.Fatal(err)
			}

			dataType, found := typeMap.TypeForName(name)
			if !found {
				t.Fatal("missing type", name)
			}

			var nullable ratel.Null[*jsonValue]
			if err := typeMap.Scan(dataType.OID, pgtype.TextFormatCode, []byte("null"), &nullable); err != nil {
				t.Fatal(err)
			}

			if !nullable.Valid || nullable.V != nil {
				t.Fatalf("JSON null became SQL NULL: %+v", nullable)
			}
		})
	}
}

func BenchmarkValueCodec(b *testing.B) {
	b.Run("pgx", func(b *testing.B) {
		b.Helper()
		benchmarkValueCodec(b, int64(42))
	})
	b.Run("Null", func(b *testing.B) {
		b.Helper()
		benchmarkValueCodec(b, ratel.NullFrom(int64(42)))
	})
	b.Run("Opt", func(b *testing.B) {
		b.Helper()
		benchmarkValueCodec(b, ratel.OptFrom(int64(42)))
	})
}

func benchmarkValueCodec[T any](b *testing.B, value T) {
	b.Helper()

	typeMap := pgtype.NewMap()
	if err := ratel.RegisterValue[int64](typeMap, "int8"); err != nil {
		b.Fatal(err)
	}

	buffer := make([]byte, 0, 8)

	for b.Loop() {
		encoded, err := typeMap.Encode(pgtype.Int8OID, pgtype.BinaryFormatCode, value, buffer[:0])
		if err != nil {
			b.Fatal(err)
		}

		if err := typeMap.Scan(pgtype.Int8OID, pgtype.BinaryFormatCode, encoded, &value); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNull(b *testing.B) {
	for b.Loop() {
		value := ratel.NullFrom(42)
		_ = value.IsZero()
	}
}

func BenchmarkOpt(b *testing.B) {
	for b.Loop() {
		value := ratel.OptFrom(42)
		_ = value.IsZero()
	}
}
