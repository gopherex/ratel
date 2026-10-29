package gengo_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

func TestIndent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, code, want string }{
		{"empty", "", ""},
		{"lines", "\ta\n\tb\n", "\t\ta\n\t\tb\n"},
		{"blank line kept blank", "\ta\n\n\tb\n", "\t\ta\n\n\t\tb\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := gengo.Indent(tc.code); got != tc.want {
				t.Fatalf("Indent(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

func BenchmarkIndent(b *testing.B) {
	for b.Loop() {
		gengo.Indent("\t\tvalue, err := f()\n\t\tif err != nil {\n\t\t\treturn model, err\n\t\t}\n")
	}
}
