package catalog_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestCatalog(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	for _, tc := range []struct {
		name string
		run  func(*testing.T, parse.Parser)
	}{
		{"Annotations", testAnnotations},
		{"AnnotationTargets", testAnnotationTargets},
		{"Build", testBuild},
		{"Permutations", testPermutations},
		{"Version", testVersion},
		{"SequenceDefaults", testSequenceDefaults},
		{"GeneratedMetadata", testGeneratedMetadata},
		{"AlterPrimaryNotNull", testAlterPrimaryNotNull},
		{"Declarations", testDeclarations},
		{"Attachments", testAttachments},
		{"IntegrationFixturePermutations", testIntegrationFixturePermutations},
		{"Normalization", testNormalization},
		{"UnnamedIndexes", testUnnamedIndexes},
		{"Resolution", testResolution},
		{"BuiltinSnapshot", testBuiltinSnapshot},
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
