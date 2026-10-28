package mapper_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestMapper(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	for _, tc := range []struct {
		name string
		run  func(*testing.T, parse.Parser)
	}{
		{"MapDDL", testMapDDL},
		{"ColumnConstraints", testColumnConstraints},
		{"MapExpr", testMapExpr},
		{"StatementProvenance", testStatementProvenance},
		{"SourceCoordinates", testSourceCoordinates},
		{"NilDeclarations", testNilDeclarations},
		{"MapRoutine", testMapRoutine},
		{"FunctionReturns", testFunctionReturns},
		{"SchemaDeclarations", testSchemaDeclarations},
		{"AlterTableFragments", testAlterTableFragments},
		{"UnrepresentableGrants", testUnrepresentableGrants},
		{"TypeObjectReferences", testTypeObjectReferences},
		{"MapStatements", testMapStatements},
		{"MapType", testMapType},
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
