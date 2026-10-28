package annotate_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testScan(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, tc := range []struct {
		name, sql, value, failure string
		count                     int
	}{
		{name: "quoted", sql: `-- @ratel.backfill expr="a ""quoted"" value" batch=10`, value: `a "quoted" value`, count: 1},
		{name: "block", sql: "/* @ratel.origin proto.Message */", value: "proto.Message", count: 1},
		{name: "SQL literals", sql: `SELECT '@ratel.bad', $$-- @ratel.bad$$, "@ratel.bad";`},
		{name: "unterminated quote", sql: `-- @ratel.origin "broken`, failure: "quoted"},
		{name: "missing value", sql: `-- @ratel.rel name=`, failure: "value"},
		{name: "duplicate key", sql: `-- @ratel.rel name=A name=B`, failure: "duplicate"},
		{name: "Unicode whitespace", sql: "-- @ratel.origin proto.Message", value: "proto.Message", count: 1},
		{name: "Unicode argument separator", sql: "-- @ratel.rel name=A inverse=B", value: "A", count: 1},
		{name: "quoted key", sql: `-- @ratel.rel "name"=A`, failure: "key"},
		{name: "block lines", sql: "/*\n * @ratel.origin A\n */", value: "A", count: 1},
		{name: "prose", sql: "-- mention @ratel.bad in prose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := annotate.Scan(parser, "source.sql", tc.sql)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("error = %v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(result) != tc.count {
				t.Fatalf("count = %d, want %d", len(result), tc.count)
			}

			if tc.count > 0 && result[0].Value.GetArgs()[0].GetValue() != tc.value {
				t.Fatalf("annotation = %v", result[0].Value)
			}
		})
	}
}

func BenchmarkScan(b *testing.B) {
	parser := newParser(b)

	sql := "-- @ratel.origin proto.Message\nCREATE TABLE a(id int);"
	for b.Loop() {
		if _, err := annotate.Scan(parser, "schema.sql", sql); err != nil {
			b.Fatal(err)
		}
	}
}
