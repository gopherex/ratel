package overload_test

import (
	"errors"
	"testing"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/objectid"
	"github.com/gopherex/ratel/cmd/ratel/internal/overload"
)

func resolver(tb testing.TB) *overload.Resolver {
	tb.Helper()

	schemas, err := builtins.For(18)
	if err != nil {
		tb.Fatal(err)
	}

	return overload.New(&ir.Catalog{}, schemas)
}

func types(names ...string) []*ir.TypeRef {
	result := make([]*ir.TypeRef, len(names))

	for index, name := range names {
		if name != "" {
			result[index] = overload.Scalar(name)
		}
	}

	return result
}

// Each case is verified against PostgreSQL 18 (SELECT pg_typeof(...) and
// the error it raises).
func TestFunction(t *testing.T) {
	t.Parallel()

	res := resolver(t)

	for _, tc := range []struct {
		name, function string
		args           []*ir.TypeRef
		target, result string
		ambiguous      bool
		missing        bool
	}{
		{name: "exact", function: "length", args: types("text"), target: "pg_catalog.length(text)", result: "int4"},
		{
			name: "implicit cast", function: "lower", args: types("varchar"),
			target: "pg_catalog.lower(text)", result: "text",
		},
		{
			name: "unknown prefers the string category", function: "length", args: types(""),
			target: "pg_catalog.length(text)", result: "int4",
		},
		{
			name: "unknown beats ranges", function: "lower", args: types(""),
			target: "pg_catalog.lower(text)", result: "text",
		},
		{name: "integer literal", function: "abs", args: types("int4"), target: "pg_catalog.abs(int4)", result: "int4"},
		{name: "widened argument", function: "abs", args: types("int2"), target: "pg_catalog.abs(int2)", result: "int2"},
		{
			name: "polymorphic instantiation", function: "array_append",
			args:   []*ir.TypeRef{overload.Array(overload.Scalar("int4")), overload.Scalar("int4")},
			target: "pg_catalog.array_append(anycompatiblearray,anycompatible)", result: "int4[]",
		},
		{name: "no candidate", function: "lower", args: types("int4"), missing: true},
		{name: "undeclared", function: "absent", args: types(), missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chosen, err := res.Function([]string{tc.function}, nil, tc.args, nil, false)

			var refused *overload.Error

			switch {
			case tc.missing || tc.ambiguous:
				if !errors.As(err, &refused) || refused.Ambiguous != tc.ambiguous {
					t.Fatalf("error = %v, want ambiguous=%v", err, tc.ambiguous)
				}
			case err != nil:
				t.Fatal(err)
			default:
				got := objectid.ID(&ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_FUNCTION, Name: chosen.Function.GetName()})
				if got != tc.target || overload.TypeKey(chosen.Result) != tc.result {
					t.Fatalf("chose %s -> %s, want %s -> %s", got, overload.TypeKey(chosen.Result), tc.target, tc.result)
				}
			}
		})
	}
}

func TestOperator(t *testing.T) {
	t.Parallel()

	res := resolver(t)

	for _, tc := range []struct {
		name, operator string
		args           []*ir.TypeRef
		result         string
		missing        bool
	}{
		{name: "unknown operand takes the known type", operator: "||", args: types("text", ""), result: "text"},
		{name: "both unknown concatenate as text", operator: "||", args: types("", ""), result: "text"},
		{name: "mixed integers widen", operator: "+", args: types("int4", "int8"), result: "int8"},
		{name: "prefix minus", operator: "-", args: types("numeric"), result: "numeric"},
		{name: "comparison", operator: "<", args: types("text", "text"), result: "bool"},
		{name: "no operator", operator: "+", args: types("bool", "bool"), missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chosen, err := res.Operator(tc.operator, tc.args)

			if tc.missing {
				var refused *overload.Error
				if !errors.As(err, &refused) || refused.Ambiguous {
					t.Fatalf("error = %v, want no operator", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if overload.TypeKey(chosen.Result) != tc.result {
				t.Fatalf("result = %s, want %s", overload.TypeKey(chosen.Result), tc.result)
			}
		})
	}
}

func TestCommonAndCoercion(t *testing.T) {
	t.Parallel()

	res := resolver(t)

	for _, tc := range []struct {
		name    string
		values  []*ir.TypeRef
		want    string
		problem bool
	}{
		{name: "widest numeric", values: types("int4", "int8", "numeric"), want: "numeric"},
		{name: "unknowns default to text", values: types("", ""), want: "text"},
		{name: "unknown takes the known", values: types("", "int4"), want: "int4"},
		{name: "incompatible", values: types("int4", "bool"), problem: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, problem := res.Common(tc.values)
			if (problem != "") != tc.problem || (!tc.problem && overload.TypeKey(got) != tc.want) {
				t.Fatalf("Common = %s, %q; want %s, problem %v", overload.TypeKey(got), problem, tc.want, tc.problem)
			}
		})
	}

	narrow, wide := overload.Scalar("int4"), overload.Scalar("int8")
	if !res.Coercible(narrow, wide) || res.Coercible(wide, narrow) {
		t.Fatal("int4 -> int8 is implicit, int8 -> int4 is not")
	}

	if !res.IsOperator("||") || res.IsOperator("no-such-op") {
		t.Fatal("IsOperator")
	}

	if len(res.Functions([]string{"pg_catalog", "in_range"})) == 0 {
		t.Fatal("Functions(pg_catalog.in_range)")
	}
}

func BenchmarkFunction(b *testing.B) {
	res := resolver(b)
	args := types("")

	for b.Loop() {
		if _, err := res.Function([]string{"lower"}, nil, args, nil, false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOperator(b *testing.B) {
	res := resolver(b)
	args := types("text", "")

	for b.Loop() {
		if _, err := res.Operator("||", args); err != nil {
			b.Fatal(err)
		}
	}
}
