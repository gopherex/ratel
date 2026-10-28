package migrate_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
)

func TestSplit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, sql string
		want      []migrate.Piece
	}{
		{name: "empty", sql: "  \n"},
		{
			name: "statements and comments", sql: "-- a\nSELECT 1;\n  -- b\nSELECT 2",
			want: []migrate.Piece{
				{Text: "-- a", Comment: true}, {Text: "SELECT 1"}, {Text: "-- b", Comment: true}, {Text: "SELECT 2"},
			},
		},
		{
			name: "quotes", sql: `SELECT ';', "a;b", 'it''s;';SELECT 2;`,
			want: []migrate.Piece{{Text: `SELECT ';', "a;b", 'it''s;'`}, {Text: "SELECT 2"}},
		},
		{
			name: "dollar quotes", sql: "CREATE FUNCTION f() AS $body$ SELECT 1; $body$;\nSELECT $1, $$;$$;",
			want: []migrate.Piece{{Text: "CREATE FUNCTION f() AS $body$ SELECT 1; $body$"}, {Text: "SELECT $1, $$;$$"}},
		},
		{
			name: "comments inside", sql: "SELECT 1 -- ;\n, /* ; */ 2;",
			want: []migrate.Piece{{Text: "SELECT 1 -- ;\n, /* ; */ 2"}},
		},
		{name: "unterminated", sql: "SELECT 'x", want: []migrate.Piece{{Text: "SELECT 'x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if difference := cmp.Diff(tc.want, migrate.Split(tc.sql), cmpopts.EquateEmpty()); difference != "" {
				t.Errorf("split() (-want +got):\n%s", difference)
			}
		})
	}
}

func BenchmarkSplit(b *testing.B) {
	sql := "CREATE FUNCTION f() AS $body$ SELECT 1; $body$;\nSELECT 'a;b', \"c\";\n-- note\nSELECT 2;\n"

	for b.Loop() {
		migrate.Split(sql)
	}
}
