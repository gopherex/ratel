package migrate_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gopherex/ratel/pkg/migrate"
)

func TestReadAndVerify(t *testing.T) {
	t.Parallel()

	directory := fstest.MapFS{
		"2_b.sql":   {Data: []byte("SELECT 2;")},
		"1_a.sql":   {Data: []byte("SELECT 1;")},
		"ratel.sum": {Data: []byte("ignored")},
	}

	sources, err := migrate.Read(directory)
	if err != nil || len(sources) != 2 || sources[0].Name != "1_a.sql" {
		t.Fatalf("Read() = %v, %v", sources, err)
	}

	recorded := migrate.Sum(sources)

	for _, tc := range []struct {
		name    string
		sources []migrate.Source
		sum     []byte
		problem string
	}{
		{name: "matching", sources: sources, sum: recorded},
		{
			name: "edited", sources: []migrate.Source{sources[0], {Name: "2_b.sql", SQL: "SELECT 3;"}}, sum: recorded,
			problem: "2_b.sql: edited",
		},
		{
			name: "added", sources: append(sources[:2:2], migrate.Source{Name: "3_c.sql"}), sum: recorded,
			problem: "3_c.sql: not in ratel.sum",
		},
		{name: "missing", sources: sources[:1], sum: recorded, problem: "2_b.sql: missing"},
		{
			name: "sum edited", sources: sources,
			sum:     []byte(strings.Replace(string(recorded), "directory h1:", "directory h1:x", 1)),
			problem: "ratel.sum: edited",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := migrate.Verify(tc.sources, tc.sum)

			switch {
			case tc.problem == "" && err != nil:
				t.Fatalf("Verify() = %v", err)
			case tc.problem != "" && (!errors.Is(err, migrate.ErrSum) || !strings.Contains(err.Error(), tc.problem)):
				t.Fatalf("Verify() = %v, want %q", err, tc.problem)
			}
		})
	}
}

func BenchmarkSum(b *testing.B) {
	sources := []migrate.Source{{Name: "1_a.sql", SQL: strings.Repeat("SELECT 1;\n", 1000)}}

	for b.Loop() {
		migrate.Sum(sources)
	}
}
