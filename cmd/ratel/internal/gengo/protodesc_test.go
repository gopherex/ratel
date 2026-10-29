package gengo_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

func TestProtoPresent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, base string
		null, lazy bool
		want       string
	}{
		{name: "lazy", lazy: true, want: "r != nil"},
		{name: "nullable", base: "time.Time", null: true, want: "r.Valid"},
		{name: "numeric", base: "pgtype.Numeric", want: "r.Valid"},
		{name: "slice", base: "[]string", want: "len(r) > 0"},
		{name: "bool", base: "bool", want: "r"},
		{name: "string", base: "string", want: `r != ""`},
		{name: "number", base: "int64", want: "r != 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := gengo.ProtoPresent(tc.base, tc.null, tc.lazy, "r"); got != tc.want {
				t.Fatalf("present = %q, want %q", got, tc.want)
			}
		})
	}
}

func BenchmarkProtoPresent(b *testing.B) {
	for b.Loop() {
		gengo.ProtoPresent("[]string", false, false, "r")
	}
}
