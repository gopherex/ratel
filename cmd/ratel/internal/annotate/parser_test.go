package annotate_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestAnnotate(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	for _, tc := range []struct {
		name string
		run  func(*testing.T, parse.Parser)
	}{
		{"Attach", testAttach},
		{"Diagnostic", testDiagnostic},
		{"Queries", testQueries},
		{"QueryNamespaces", testQueryNamespaces},
		{"QueryCommands", testQueryCommands},
		{"QueryFiles", testQueryFiles},
		{"Scan", testScan},
		{"Validate", testValidate},
		{"ValidateLabels", testValidateLabels},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.run(t, parser)
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
