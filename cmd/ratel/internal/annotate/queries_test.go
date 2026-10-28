package annotate_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testQueries(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		name, sql, failure string
		count              int
	}{
		{name: "split", sql: "-- name: ByID :one\n-- @ratel.row User\nSELECT @id;\n" +
			"-- name: List :many\nSELECT $$-- name: False :one$$;", count: 2},
		{name: "invalid name", sql: "-- name: bad :one\nSELECT 1;", failure: "name"},
		{name: "invalid command", sql: "-- name: Good :bad\nSELECT 1;", failure: "command"},
		{name: "duplicate", sql: "-- name: Good :one\nSELECT 1;\n-- name: Good :one\nSELECT 2;", failure: "duplicate"},
		{name: "unnamed", sql: "SELECT 1;", failure: "header"},
		{name: "multiple statements", sql: "-- name: Good :one\nSELECT 1; SELECT 2;", failure: "statement"},
		{name: "empty", sql: "-- name: Good :one", failure: "empty"},
		{name: "wrong annotation", sql: "-- name: Good :one\n-- @ratel.origin A\nSELECT 1;", failure: "target"},
		{
			name: "conflict", sql: "-- name: Good :one\n-- @ratel.nullable id\n-- @ratel.notnull id\nSELECT @id;",
			failure: "conflict",
		},
		{name: "duplicate row", sql: "-- name: Good :one\n-- @ratel.row A\n-- @ratel.row B\nSELECT 1;", failure: "duplicate"},
		{name: "annotation after SQL", sql: "-- name: Good :one\nSELECT 1;\n-- @ratel.row A", failure: "precede"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := annotate.Queries(parser, []annotate.File{{Name: "queries.sql", SQL: tc.sql}}, nil)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("error = %v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(result.Queries) != tc.count {
				t.Fatalf("count = %d", len(result.Queries))
			}

			if tc.count > 0 {
				query := result.Queries[0]
				if query.SQL != "SELECT @id;" || query.Meta.GetAnnotations()[0].GetQueryName() != "ByID" {
					t.Fatalf("query = %v", query)
				}

				if query.Meta.GetSource().GetStartLine() != 3 {
					t.Fatalf("span = %v", query.Meta.GetSource())
				}
			}
		})
	}
}

func testQueryNamespaces(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		name     string
		owned    []string
		warnings int
	}{
		{name: "unowned", warnings: 1},
		{name: "owned", owned: []string{"go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			files := []annotate.File{{Name: "q.sql", SQL: "-- name: Result :one\n-- @go.row A\nSELECT 1;"}}

			result, err := annotate.Queries(parser, files, tc.owned)
			if err != nil {
				t.Fatal(err)
			}

			if len(result.Diagnostics) != tc.warnings {
				t.Fatalf("warnings = %v", result.Diagnostics)
			}

			if result.Queries[0].Meta.GetAnnotations()[0].GetNamespace() != "go" {
				t.Fatal("plugin metadata lost")
			}
		})
	}
}

func testQueryCommands(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		command string
		want    ir.QueryCommand
	}{
		{":one", ir.QueryCommand_QUERY_COMMAND_ONE},
		{":many", ir.QueryCommand_QUERY_COMMAND_MANY},
		{":exec", ir.QueryCommand_QUERY_COMMAND_EXEC},
		{":execrows", ir.QueryCommand_QUERY_COMMAND_EXEC_ROWS},
		{":copyfrom", ir.QueryCommand_QUERY_COMMAND_COPY_FROM},
	} {
		t.Run(tc.command, func(t *testing.T) {
			t.Parallel()

			sql := "-- name: Query " + tc.command + "\nSELECT 1 -- name: NotAHeader :bad\n;"

			result, err := annotate.Queries(parser, []annotate.File{{Name: "q.sql", SQL: sql}}, nil)
			if err != nil {
				t.Fatal(err)
			}

			if len(result.Queries) != 1 || result.Queries[0].Command != tc.want {
				t.Fatalf("result = %v", result)
			}
		})
	}
}

func testQueryFiles(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		name, second string
		duplicate    bool
	}{
		{"unique", "Second", false},
		{"duplicate", "First", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			files := []annotate.File{
				{Name: "z.sql", SQL: "-- name: " + tc.second + " :exec\nSELECT 2;"},
				{Name: "a.sql", SQL: "-- name: First :exec\nSELECT 1;"},
			}

			result, err := annotate.Queries(parser, files, nil)
			if tc.duplicate {
				if err == nil || !strings.Contains(err.Error(), "z.sql:1:1") ||
					!strings.Contains(err.Error(), "a.sql:1:1") {
					t.Fatalf("error = %v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if result.Queries[0].Name != "First" || result.Queries[1].Name != "Second" {
				t.Fatalf("result = %v", result)
			}
		})
	}
}

func BenchmarkQueries(b *testing.B) {
	parser := newParser(b)

	files := []annotate.File{{Name: "queries.sql", SQL: "-- name: ByID :one\nSELECT @id;"}}
	for b.Loop() {
		if _, err := annotate.Queries(parser, files, nil); err != nil {
			b.Fatal(err)
		}
	}
}
