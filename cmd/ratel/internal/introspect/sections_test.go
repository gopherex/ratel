package introspect_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestSections(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	seen := make(map[string]bool)

	for _, section := range introspect.Sections() {
		name, query, mode := section[0], section[1], section[2]
		if seen[name] {
			t.Fatalf("duplicate section %s", name)
		}

		seen[name] = true

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if namespaced := strings.Contains(query, "$1"); namespaced != (mode == "namespaces") {
				t.Fatalf("query uses $1 = %v, mode = %s", namespaced, mode)
			}

			// Every query is valid PostgreSQL, checked by its own parser.
			if _, err := parser.Parse(query); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(query, "ORDER BY") {
				t.Fatal("query has no deterministic order")
			}
		})
	}
}

func newParser(tb testing.TB) parse.Parser {
	tb.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		tb.Fatal(err)
	}

	return parser
}

func BenchmarkSections(b *testing.B) {
	for b.Loop() {
		if len(introspect.Sections()) == 0 {
			b.Fatal("no sections")
		}
	}
}
