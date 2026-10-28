package parse_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestStatements(t *testing.T) {
	t.Parallel()

	// wazero lazily caches its version in an unsynchronized global on the
	// first runtime creation; one serial parse before parallel use avoids
	// that race (see parse.Warm).
	parse.Warm()

	for _, tc := range []struct {
		name    string
		sql     string
		count   int
		invalid bool
	}{
		{name: "empty"},
		{name: "select", sql: "SELECT 1", count: 1},
		{name: "multiple", sql: "CREATE TABLE a(id int); SELECT 1", count: 2},
		{name: "invalid", sql: "SELECT FROM", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statements, err := parse.Statements(tc.sql)
			if (err != nil) != tc.invalid {
				t.Fatalf("Statements() error = %v, invalid = %v", err, tc.invalid)
			}

			if len(statements) != tc.count {
				t.Fatalf("Statements() count = %d, want %d", len(statements), tc.count)
			}

			for index, statement := range statements {
				if statement.Index != index || statement.Node == nil {
					t.Fatalf("statement %d = %+v", index, statement)
				}
			}
		})
	}
}

func BenchmarkStatements(b *testing.B) {
	for b.Loop() {
		_, err := parse.Statements("SELECT 1")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParse(b *testing.B) {
	for b.Loop() {
		_, err := parse.Parse("SELECT 1")
		if err != nil {
			b.Fatal(err)
		}
	}
}
