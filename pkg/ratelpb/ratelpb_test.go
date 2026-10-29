package ratelpb_test

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/gopherex/ratel/pkg/ratelpb"
)

func TestSet(t *testing.T) {
	t.Parallel()

	if ratelpb.Set(false, "x") != nil || ratelpb.Set(true, "x") != "x" {
		t.Fatal("Set does not follow ok")
	}
}

func TestTime(t *testing.T) {
	t.Parallel()

	moment := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	if ratelpb.Time(nil) != nil || ratelpb.FromTime(nil) != nil {
		t.Fatal("nil is not NULL")
	}

	if got := ratelpb.Time(timestamppb.New(moment)); got != moment {
		t.Fatalf("Time() = %v", got)
	}

	if got := ratelpb.FromTime(&moment); !got.AsTime().Equal(moment) {
		t.Fatalf("FromTime() = %v", got)
	}
}

func TestInterval(t *testing.T) {
	t.Parallel()

	if ratelpb.Interval(nil) != nil {
		t.Fatal("nil is not NULL")
	}

	for _, tc := range []struct {
		name     string
		duration *durationpb.Duration
		want     pgtype.Interval
		err      error
	}{
		{"microseconds", durationpb.New(90 * time.Second), pgtype.Interval{Microseconds: 90e6, Valid: true}, nil},
		{"negative", durationpb.New(-time.Millisecond), pgtype.Interval{Microseconds: -1000, Valid: true}, nil},
		{"nanoseconds", durationpb.New(time.Nanosecond), pgtype.Interval{}, ratelpb.ErrSubMicrosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			valuer, ok := ratelpb.Interval(tc.duration).(pgtype.IntervalValuer)
			if !ok {
				t.Fatal("Interval() is not a pgtype.IntervalValuer")
			}

			got, err := valuer.IntervalValue()
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("IntervalValue() = %+v, %v", got, err)
			}
		})
	}

	invalid := &durationpb.Duration{Seconds: 1, Nanos: -1}
	if _, err := ratelpb.Interval(invalid).(pgtype.IntervalValuer).IntervalValue(); err == nil {
		t.Fatal("invalid Duration encoded")
	}
}

func TestFromInterval(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		interval pgtype.Interval
		want     time.Duration
		err      error
	}{
		{"days as 24 hours", pgtype.Interval{Days: 1, Microseconds: 5, Valid: true}, 24*time.Hour + 5*time.Microsecond, nil},
		{"months", pgtype.Interval{Months: 1, Valid: true}, 0, ratelpb.ErrMonths},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ratelpb.FromInterval(tc.interval)
			if !errors.Is(err, tc.err) || (err == nil && got.AsDuration() != tc.want) {
				t.Fatalf("FromInterval() = %v, %v", got, err)
			}
		})
	}
}

// encoded is the JSON pgx would send for a write value.
func encoded(t *testing.T, value any) []byte {
	t.Helper()

	marshaler, ok := value.(json.Marshaler)
	if !ok {
		t.Fatalf("%T is not a json.Marshaler", value)
	}

	data, err := marshaler.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestJSON(t *testing.T) {
	t.Parallel()

	if ratelpb.JSON(nil) != nil || ratelpb.JSON((*structpb.Struct)(nil)) != nil {
		t.Fatal("absent message is not NULL")
	}

	message, err := structpb.NewStruct(map[string]any{"a": 1.5})
	if err != nil {
		t.Fatal(err)
	}

	back, err := ratelpb.FromJSON[structpb.Struct](encoded(t, ratelpb.JSON(message)))
	if err != nil || !proto.Equal(back, message) {
		t.Fatalf("round trip = %v, %v", back, err)
	}

	if _, err := ratelpb.FromJSON[structpb.Struct]([]byte(`[`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestJSONList(t *testing.T) {
	t.Parallel()

	list := []*wrapperspb.StringValue{wrapperspb.String("a"), wrapperspb.String("b")}

	back, err := ratelpb.FromJSONList[wrapperspb.StringValue](encoded(t, ratelpb.JSONList(list)))
	if err != nil || len(back) != 2 || back[1].GetValue() != "b" {
		t.Fatalf("round trip = %v, %v", back, err)
	}

	if string(encoded(t, ratelpb.JSONList([]*wrapperspb.StringValue{}))) != "[]" {
		t.Fatal("empty list is not []")
	}

	for _, data := range []string{`{}`, `[{"value": 1}]`} {
		if _, err := ratelpb.FromJSONList[wrapperspb.StringValue]([]byte(data)); err == nil {
			t.Fatalf("%s accepted", data)
		}
	}
}

func TestJSONMap(t *testing.T) {
	t.Parallel()

	values := map[int64]*wrapperspb.Int64Value{7: wrapperspb.Int64(70)}

	back, err := ratelpb.FromJSONMap[int64, wrapperspb.Int64Value](encoded(t, ratelpb.JSONMap(values)))
	if err != nil || back[7].GetValue() != 70 {
		t.Fatalf("round trip = %v, %v", back, err)
	}

	for _, data := range []string{`[]`, `{"7": {"x": 1}}`} {
		if _, err := ratelpb.FromJSONMap[int64, wrapperspb.Int64Value]([]byte(data)); err == nil {
			t.Fatalf("%s accepted", data)
		}
	}
}

func TestScalarMap(t *testing.T) {
	t.Parallel()

	back, err := ratelpb.FromScalarMap[string, int32](encoded(t, ratelpb.ScalarMap(map[string]int32{"a": 1})))
	if err != nil || back["a"] != 1 {
		t.Fatalf("round trip = %v, %v", back, err)
	}

	if string(encoded(t, ratelpb.ScalarMap[string, int32](nil))) != "{}" {
		t.Fatal("nil map is not {}")
	}

	if _, err := ratelpb.FromScalarMap[string, int32]([]byte(`[]`)); err == nil {
		t.Fatal("array accepted")
	}
}

type color int32

func (c color) String() string { return [...]string{"COLOR_UNSPECIFIED", "COLOR_RED"}[c] }

// colorValue is the generated <Enum>_value map of color.
func colorValue() map[string]int32 { return map[string]int32{"COLOR_UNSPECIFIED": 0, "COLOR_RED": 1} }

func TestEnums(t *testing.T) {
	t.Parallel()

	names := ratelpb.EnumNames([]color{1, 0})
	if len(names) != 2 || names[0] != "COLOR_RED" {
		t.Fatalf("EnumNames() = %v", names)
	}

	back, err := ratelpb.FromEnums[color](colorValue(), names)
	if err != nil || len(back) != 2 || back[0] != 1 {
		t.Fatalf("FromEnums() = %v, %v", back, err)
	}

	if _, err := ratelpb.FromEnum[color](colorValue(), "COLOR_BLUE"); !errors.Is(err, ratelpb.ErrUnknownEnum) {
		t.Fatalf("unknown name = %v", err)
	}

	if _, err := ratelpb.FromEnums[color](colorValue(), []string{"COLOR_BLUE"}); !errors.Is(err, ratelpb.ErrUnknownEnum) {
		t.Fatalf("unknown names = %v", err)
	}
}

func TestWrapped(t *testing.T) {
	t.Parallel()

	if ratelpb.Wrapped((*wrapperspb.Int32Value)(nil)) != nil || ratelpb.Wrapped(wrapperspb.Int32(4)) != int32(4) {
		t.Fatal("Wrapped does not unwrap")
	}
}

func TestUint32(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value int64
		err   error
	}{{0, nil}, {math.MaxUint32, nil}, {-1, ratelpb.ErrOutOfRange}, {math.MaxUint32 + 1, ratelpb.ErrOutOfRange}} {
		got, err := ratelpb.Uint32(tc.value)
		if !errors.Is(err, tc.err) || (err == nil && int64(got) != tc.value) {
			t.Fatalf("Uint32(%d) = %d, %v", tc.value, got, err)
		}
	}
}

func TestUint64(t *testing.T) {
	t.Parallel()

	maximum := new(big.Int).SetUint64(math.MaxUint64)

	for _, tc := range []struct {
		name    string
		numeric pgtype.Numeric
		want    uint64
		err     error
	}{
		{"round trip", ratelpb.Numeric(math.MaxUint64), math.MaxUint64, nil},
		{"positive exponent", pgtype.Numeric{Int: big.NewInt(12), Exp: 2, Valid: true}, 1200, nil},
		{"integral fraction", pgtype.Numeric{Int: big.NewInt(1200), Exp: -2, Valid: true}, 12, nil},
		{"fraction", pgtype.Numeric{Int: big.NewInt(1201), Exp: -2, Valid: true}, 0, ratelpb.ErrOutOfRange},
		{"negative", pgtype.Numeric{Int: big.NewInt(-1), Valid: true}, 0, ratelpb.ErrOutOfRange},
		{"too large", pgtype.Numeric{Int: maximum, Exp: 1, Valid: true}, 0, ratelpb.ErrOutOfRange},
		{"NaN", pgtype.Numeric{NaN: true, Valid: true}, 0, ratelpb.ErrOutOfRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ratelpb.Uint64(tc.numeric)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("Uint64() = %d, %v", got, err)
			}
		})
	}
}

func BenchmarkFromJSONList(b *testing.B) {
	list := []*wrapperspb.StringValue{wrapperspb.String("a"), wrapperspb.String("b"), wrapperspb.String("c")}

	data, err := ratelpb.JSONList(list).(json.Marshaler).MarshalJSON()
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if _, err := ratelpb.FromJSONList[wrapperspb.StringValue](data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUint64(b *testing.B) {
	numeric := pgtype.Numeric{Int: big.NewInt(1200), Exp: -2, Valid: true}

	for b.Loop() {
		if _, err := ratelpb.Uint64(numeric); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFromEnums(b *testing.B) {
	names := []string{"COLOR_RED", "COLOR_UNSPECIFIED", "COLOR_RED"}

	for b.Loop() {
		if _, err := ratelpb.FromEnums[color](colorValue(), names); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFromInterval(b *testing.B) {
	interval := pgtype.Interval{Days: 1, Microseconds: 5, Valid: true}

	for b.Loop() {
		if _, err := ratelpb.FromInterval(interval); err != nil {
			b.Fatal(err)
		}
	}
}
