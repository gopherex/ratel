package lint_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/lint"
)

// TestEveryCodeDescribed checks each documented code has its own text.
func TestEveryCodeDescribed(t *testing.T) {
	t.Parallel()

	var codes []string

	for area, count := range map[string]int{"L": 10, "R": 4, "D": 5, "C": 8, "T": 6, "P": 2} {
		for number := 1; number <= count; number++ {
			codes = append(codes, "RTL-"+area+"0"+string(rune('0'+number%10)))
		}
	}

	seen := make(map[string]string)

	for _, code := range codes {
		if code == "RTL-L00" {
			code = "RTL-L10"
		}

		text := lint.Detail(code)
		if strings.HasPrefix(text, "rule ") || seen[text] != "" {
			t.Errorf("%s: %q (also %s)", code, text, seen[text])
		}

		seen[text] = code
	}
}

func BenchmarkDetail(b *testing.B) {
	for b.Loop() {
		lint.Detail("RTL-P02")
	}
}
