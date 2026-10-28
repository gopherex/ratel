package catalog_test

import (
	"bytes"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testIntegrationFixturePermutations(t *testing.T, parser parse.Parser) {
	t.Helper()

	data, err := os.ReadFile("../../../../tests/testdata/catalog/schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	sql := string(data)

	parsed, err := parser.Parse(sql)
	if err != nil {
		t.Fatal(err)
	}

	files := make([]catalog.File, 0, len(parsed.GetStmts()))
	statements := make([]string, 0, len(parsed.GetStmts()))

	for ordinal, raw := range parsed.GetStmts() {
		start, end := int(raw.GetStmtLocation()), len(sql)
		if raw.GetStmtLen() != 0 {
			end = start + int(raw.GetStmtLen())
		}

		statement := strings.TrimSuffix(strings.TrimSpace(sql[start:end]), ";") + ";"
		statements = append(statements, statement)
		files = append(files, catalog.File{Name: strconv.Itoa(ordinal) + ".sql", SQL: statement})
	}

	expected := buildBytes(t, parser, files, false)
	oneFile := []catalog.File{{Name: "fixture.sql", SQL: strings.Join(statements, "\n")}}
	expectedStatements := buildBytes(t, parser, oneFile, true)

	random := rand.New(rand.NewPCG(3, 4))
	for range 30 {
		random.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })

		if !bytes.Equal(expected, buildBytes(t, parser, files, false)) {
			t.Fatal("fixture file order changes catalog")
		}

		random.Shuffle(len(statements), func(i, j int) { statements[i], statements[j] = statements[j], statements[i] })

		oneFile[0].SQL = strings.Join(statements, "\n")
		if !bytes.Equal(expectedStatements, buildBytes(t, parser, oneFile, true)) {
			t.Fatal("fixture statement order changes catalog")
		}
	}
}
