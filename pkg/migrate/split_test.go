package migrate_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/gopherex/ratel/pkg/migrate"
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
		{
			name: "escape string", sql: `SELECT E'\';', e'\\';SELECT 'a\';`,
			want: []migrate.Piece{{Text: `SELECT E'\';', e'\\'`}, {Text: `SELECT 'a\'`}},
		},
		{
			name: "no escape string after a word", sql: `SELECT date'\';SELECT E"\";SELECT 2`,
			want: []migrate.Piece{{Text: `SELECT date'\'`}, {Text: `SELECT E"\"`}, {Text: "SELECT 2"}},
		},
		{
			name: "nested comment", sql: "SELECT /* /* */ ; */ 1;SELECT 2",
			want: []migrate.Piece{{Text: "SELECT /* /* */ ; */ 1"}, {Text: "SELECT 2"}},
		},
		{
			name: "dollar inside identifier", sql: "SELECT a$$b;SELECT $$;$$",
			want: []migrate.Piece{{Text: "SELECT a$$b"}, {Text: "SELECT $$;$$"}},
		},
		{
			name: "dollar quoted non-ASCII tag", sql: "SELECT $ü$;$ü$;SELECT 2",
			want: []migrate.Piece{{Text: "SELECT $ü$;$ü$"}, {Text: "SELECT 2"}},
		},
		{name: "unterminated", sql: "SELECT 'x", want: []migrate.Piece{{Text: "SELECT 'x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := migrate.Split(tc.sql)
			if difference := cmp.Diff(tc.want, got, cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(migrate.Piece{}, "Start")); difference != "" {
				t.Errorf("split() (-want +got):\n%s", difference)
			}
		})
	}
}

// TestSplitStart checks the offsets: a comment at its dashes, a statement at
// its first byte after the whitespace before it.
func TestSplitStart(t *testing.T) {
	t.Parallel()

	sql := "-- a\n  SELECT 1;\n\n  -- b\nSELECT 2"
	got := migrate.Split(sql)
	want := []int{0, 7, 20, 25}

	if len(got) != len(want) {
		t.Fatalf("split() = %+v", got)
	}

	for index, piece := range got {
		if piece.Start != want[index] {
			t.Errorf("piece %q starts at %d, want %d", piece.Text, piece.Start, want[index])
		}
	}
}

func BenchmarkSplit(b *testing.B) {
	sql := "CREATE FUNCTION f() AS $body$ SELECT 1; $body$;\nSELECT 'a;b', \"c\";\n-- note\nSELECT 2;\n"

	for b.Loop() {
		migrate.Split(sql)
	}
}
