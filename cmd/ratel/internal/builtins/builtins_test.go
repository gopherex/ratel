package builtins_test

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/builtins/snapshot"
)

func TestFor(t *testing.T) {
	t.Parallel()

	for _, major := range []int{0, 15, 16, 17, 18, 19} {
		t.Run(strconv.Itoa(major), func(t *testing.T) {
			t.Parallel()

			schemas, err := builtins.For(major)

			if major < 16 || major > 18 {
				var unsupported *builtins.UnsupportedMajorError
				if !errors.As(err, &unsupported) || unsupported.Major != major ||
					!strings.Contains(err.Error(), strconv.Itoa(major)) {
					t.Fatalf("error=%v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(schemas) != 1 || schemas[0].GetName() != "pg_catalog" {
				t.Fatalf("schemas=%v", schemas)
			}

			checkFunctions(t, schemas[0], major)
			checkTypesAndOperators(t, schemas[0])
			checkCasts(t, schemas[0])
			schemas[0].Name = "mutated"

			again, err := builtins.For(major)
			if err != nil || again[0].GetName() != "pg_catalog" {
				t.Fatalf("shared mutable snapshot: %v", err)
			}
		})
	}
}

func checkFunctions(t *testing.T, schema *ir.Schema, major int) {
	t.Helper()

	functions := make(map[string]*ir.Function)

	for _, function := range schema.GetFunctions() {
		parts := function.GetName().GetParts()
		if len(parts) != 3 {
			t.Fatalf("invalid identity: %v", parts)
		}

		functions[parts[1]+"("+parts[2]+")"] = function
	}

	abs := functions["abs(int4)"]
	count := functions["count()"]
	series := functions["generate_series(int4,int4)"]
	interval := functions["make_interval(int4,int4,int4,int4,int4,int4,float8)"]
	ordered := functions["percentile_cont(float8,float8)"].GetAggregate()

	for _, tc := range []struct{ name, actual, expected string }{
		{"abs return", abs.GetReturns().GetScalar().GetPgName(), "int4"},
		{"abs strict", abs.GetNullInput().String(), ir.NullInputBehavior_NULL_INPUT_BEHAVIOR_STRICT.String()},
		{"abs volatility", abs.GetVolatility().String(), ir.Volatility_VOLATILITY_IMMUTABLE.String()},
		{"count return", count.GetReturns().GetScalar().GetPgName(), "int8"},
		{"count kind", count.GetRoutineKind(), "a"},
		{"count aggregate", count.GetAggregate().GetKind(), "n"},
		{"series setof", series.GetReturns().GetSetof().GetType().GetPgName(), "int4"},
		{"array signature", functions["array_to_tsvector(text[])"].GetReturns().GetScalar().GetPgName(), "tsvector"},
		{"vector signature", functions["int2vectorsend(int2vector)"].GetReturns().GetScalar().GetPgName(), "bytea"},
		{"variadic", functions["jsonb_build_array(any)"].GetVariadicElement().GetPgName(), "any"},
		{"default arguments", strconv.FormatUint(uint64(interval.GetDefaultArguments()), 10), "7"},
		{"ordered aggregate", ordered.GetKind(), "o"},
		{"direct arguments", strconv.FormatUint(uint64(ordered.GetDirectArguments()), 10), "1"},
		{"uuidv7", strconv.FormatBool(functions["uuidv7(interval)"] != nil), strconv.FormatBool(major == 18)},
	} {
		if tc.actual != tc.expected {
			t.Errorf("%s: got %q, want %q", tc.name, tc.actual, tc.expected)
		}
	}
}

func checkTypesAndOperators(t *testing.T, schema *ir.Schema) {
	t.Helper()

	var array, unary bool

	for _, typ := range schema.GetBuiltinTypes() {
		if typ.GetType().GetPgName() == "_int4" {
			array = typ.GetType().GetElement().GetPgName() == "int4"
		}
	}

	for _, operator := range schema.GetBuiltinOperators() {
		if operator.GetName() == "-" && operator.GetLeft() == nil && operator.GetRight().GetPgName() == "int4" {
			unary = operator.GetResult().GetPgName() == "int4" && len(operator.GetFunction().GetParts()) == 3
		}
	}

	if !array || !unary {
		t.Fatalf("array=%v unary=%v", array, unary)
	}
}

func checkCasts(t *testing.T, schema *ir.Schema) {
	t.Helper()

	var implicit, assignment bool

	for _, cast := range schema.GetBuiltinCasts() {
		if cast.GetContext() != "i" && cast.GetContext() != "a" {
			t.Fatal("explicit-only cast included")
		}

		if cast.GetSource().GetPgName() == "int4" && cast.GetTarget().GetPgName() == "float8" {
			implicit = cast.GetContext() == "i"
		}

		if cast.GetSource().GetPgName() == "float8" && cast.GetTarget().GetPgName() == "int4" {
			assignment = cast.GetContext() == "a"
		}
	}

	if !implicit || !assignment {
		t.Fatalf("implicit=%v assignment=%v", implicit, assignment)
	}
}

func TestSnapshotOrder(t *testing.T) {
	t.Parallel()

	for _, major := range []int{16, 17, 18} {
		t.Run(strconv.Itoa(major), func(t *testing.T) {
			t.Parallel()

			schemas, err := builtins.For(major)
			if err != nil {
				t.Fatal(err)
			}

			schema := schemas[0]

			expected, err := (proto.MarshalOptions{Deterministic: true}).Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}

			slices.Reverse(schema.GetFunctions())
			slices.Reverse(schema.GetBuiltinTypes())
			slices.Reverse(schema.GetBuiltinOperators())
			slices.Reverse(schema.GetBuiltinCasts())

			actual, err := snapshot.Encode(schema)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(expected, actual) {
				t.Fatal("reversing snapshot rows changed canonical bytes")
			}
		})
	}
}

func BenchmarkFor(b *testing.B) {
	for b.Loop() {
		if _, err := builtins.For(18); err != nil {
			b.Fatal(err)
		}
	}
}
