package parse_test

import (
	"errors"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestParser(t *testing.T) {
	// wazero lazily caches its version in an unsynchronized global on the
	// first runtime creation; one serial parse before parallel use avoids
	// that race (see parse.New).
	parser := newParser(t)
	parse.CheckConfig(t)
	t.Parallel()

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

			statements, err := parser.Statements(tc.sql)
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

	t.Run("concurrency limit", func(t *testing.T) {
		t.Parallel()
		parse.CheckConcurrencyLimit(t, parser)
	})

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		tree, err := parser.Parse("SELECT 1")
		if err != nil {
			t.Fatal(err)
		}

		sql, err := parser.Deparse(tree)
		if err != nil || sql != "SELECT 1" {
			t.Fatalf("Deparse() = %q, %v", sql, err)
		}

		tokens, err := parser.Scan(sql)
		if err != nil || len(tokens.GetTokens()) != 2 {
			t.Fatalf("Scan() = %v, %v", tokens, err)
		}
	})

	t.Run("scanner error releases slot", func(t *testing.T) {
		t.Parallel()

		if _, err := parser.Scan("'unterminated"); err == nil {
			t.Fatal("Scan() accepted an unterminated string")
		}

		if _, err := parser.Scan("SELECT 1"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConfig(t *testing.T) {
	t.Parallel()

	for _, concurrency := range []int{-1, -100} {
		t.Run("invalid "+string(rune('a'-concurrency)), func(t *testing.T) {
			t.Parallel()

			_, err := parse.New(parse.Config{Concurrency: concurrency})

			var configError *parse.ConfigError
			if !errors.As(err, &configError) || configError.Concurrency != concurrency || configError.Error() == "" {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func BenchmarkStatements(b *testing.B) {
	parser := newParser(b)
	for b.Loop() {
		if _, err := parser.Statements("SELECT 1"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParse(b *testing.B) {
	parser := newParser(b)
	for b.Loop() {
		if _, err := parser.Parse("SELECT 1"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScan(b *testing.B) {
	parser := newParser(b)
	for b.Loop() {
		if _, err := parser.Scan("SELECT 1"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDeparse(b *testing.B) {
	parser := newParser(b)

	tree, err := parser.Parse("SELECT 1")
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if _, err := parser.Deparse(tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNew(b *testing.B) {
	for b.Loop() {
		if _, err := parse.New(parse.Config{}); err != nil {
			b.Fatal(err)
		}
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
