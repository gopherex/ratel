package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func warmParser(t *testing.T) {
	t.Helper()

	if _, err := parse.New(parse.Config{}); err != nil {
		t.Fatal(err)
	}
}

func dumpFixture(tb testing.TB) string {
	tb.Helper()

	directory := tb.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "schema"), 0o750); err != nil {
		tb.Fatal(err)
	}

	sql := "-- @ratel.origin User\nCREATE TABLE public.users(id bigint PRIMARY KEY);"
	if err := os.WriteFile(filepath.Join(directory, "schema", "users.sql"), []byte(sql), 0o600); err != nil {
		tb.Fatal(err)
	}

	name := filepath.Join(directory, "ratel.yaml")
	configuration := "version: 1\npostgres: {version: 18}\nschema: [schema/**/*.sql, schema/*.sql]\n"

	if err := os.WriteFile(name, []byte(configuration), 0o600); err != nil {
		tb.Fatal(err)
	}

	return name
}

func TestDump(t *testing.T) {
	warmParser(t)

	t.Parallel()

	for _, format := range []string{"binpb", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			name := dumpFixture(t)

			var stdout, stderr bytes.Buffer

			code := cli.Main([]string{"ratel", "dump", "--config", name, "--format", format}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code %d: %s", code, &stderr)
			}

			result := new(ir.Catalog)

			var err error

			if format == "json" {
				err = protojson.Unmarshal(stdout.Bytes(), result)
			} else {
				err = proto.Unmarshal(stdout.Bytes(), result)
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(result.GetSchemas()) != 1 || len(result.GetSchemas()[0].GetTables()) != 1 {
				t.Fatal(result)
			}

			if len(result.GetSchemas()[0].GetTables()[0].GetMeta().GetAnnotations()) != 1 {
				t.Fatal("annotations missing")
			}
		})
	}
}

func TestDumpFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"format", []string{"--format", "xml"}, 2, "format"},
		{"argument", []string{"extra"}, 2, "unexpected"},
		{"config", []string{"--config", "missing-ratel.yaml"}, 1, "configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			code := cli.Main(append([]string{"ratel", "dump"}, tc.args...), &stdout, &stderr)
			if code != tc.code || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("%d %s", code, &stderr)
			}
		})
	}
}

func TestDumpConfiguration(t *testing.T) {
	warmParser(t)
	t.Parallel()

	for _, tc := range []struct {
		name, text, plugin, want string
		code                     int
	}{
		{"unmatched", "version: 1\nschema: [missing/**/*.sql]", "", "matched no files", 1},
		{"unknown key", "version: 1\ninvalid: true", "", "unknown", 1},
		{"unknown plugin", "version: 1\nschema: []", "missing", "not configured", 1},
		{"unmatched queries", "version: 1\nschema: []\nqueries: [{files: [q.sql], package: q}]", "", "matched no files", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			name := filepath.Join(t.TempDir(), "ratel.yaml")
			if err := os.WriteFile(name, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer

			args := []string{"ratel", "dump", "--config", name}
			if tc.plugin != "" {
				args = append(args, "--plugin", tc.plugin)
			}

			code := cli.Main(args, &stdout, &stderr)
			if code != tc.code || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("%d %s", code, &stderr)
			}
		})
	}
}

func BenchmarkDump(b *testing.B) {
	name := dumpFixture(b)

	for b.Loop() {
		var stdout, stderr bytes.Buffer
		if code := cli.Main([]string{"ratel", "dump", "--config", name}, &stdout, &stderr); code != 0 {
			b.Fatal(&stderr)
		}
	}
}

func BenchmarkDumpUsage(b *testing.B) {
	for b.Loop() {
		cli.Main([]string{"ratel", "dump", "--format", "xml"}, &bytes.Buffer{}, &bytes.Buffer{})
	}
}
