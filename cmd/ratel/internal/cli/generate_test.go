package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

const generateConfig = `version: 1
postgres: {version: 18}
schema: [schema/*.sql]
queries:
  - files: [queries/*.sql]
    package: users
plugins:
  - name: go
    builtin: true
    out: db
    options: {import_path: example.com/app/db}
`

const userByID = "-- name: UserByID :one\nSELECT * FROM public.users WHERE id = @id;\n"

func generateFixture(tb testing.TB, queries string) string {
	tb.Helper()

	directory := tb.TempDir()
	for name, contents := range map[string]string{
		"schema/users.sql":    "CREATE TABLE public.users(id bigint PRIMARY KEY, email text NOT NULL);",
		"queries/users.sql":   queries,
		"ratel.yaml":          generateConfig,
		"db/handwritten.txt":  "kept",
		"db/public/.keep.txt": "kept",
	} {
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			tb.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			tb.Fatal(err)
		}
	}

	return directory
}

type outcome struct {
	code   int
	stderr string
}

func run(tb testing.TB, args ...string) outcome {
	tb.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Main(append([]string{"ratel"}, args...), &stdout, &stderr)

	return outcome{code: code, stderr: stderr.String()}
}

func at(directory, path string) string { return filepath.Join(directory, filepath.FromSlash(path)) }

func TestGenerate(t *testing.T) {
	warmParser(t)

	t.Parallel()

	directory := generateFixture(t, userByID)
	config := at(directory, "ratel.yaml")

	if result := run(t, "generate", "--config", config); result.code != 0 {
		t.Fatalf("generate = %d: %s", result.code, result.stderr)
	}

	for _, path := range []string{"db/public/public.go", "db/users/users.go", "db/.ratel-manifest"} {
		if _, err := os.Stat(at(directory, path)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}

	manifest, err := os.ReadFile(at(directory, "db/.ratel-manifest"))
	if err != nil || string(manifest) != "public/public.go\nusers/users.go\n" {
		t.Fatalf("manifest = %q, %v", manifest, err)
	}

	// Without queries the users package is stale: deleted with its directory,
	// while files the run never wrote stay.
	if err := os.WriteFile(at(directory, "queries/users.sql"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if result := run(t, "generate", "--config", config); result.code != 0 {
		t.Fatalf("second generate = %d: %s", result.code, result.stderr)
	}

	for path, exists := range map[string]bool{
		"db/users": false, "db/public/public.go": true, "db/handwritten.txt": true, "db/public/.keep.txt": true,
	} {
		if _, err := os.Stat(at(directory, path)); (err == nil) != exists {
			t.Fatalf("%s exists = %v, want %v", path, err == nil, exists)
		}
	}
}

func TestGenerateRefusesSymlink(t *testing.T) {
	warmParser(t)

	t.Parallel()

	directory := generateFixture(t, "")
	outside := t.TempDir()

	if err := os.RemoveAll(at(directory, "db/public")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, at(directory, "db/public")); err != nil {
		t.Fatal(err)
	}

	result := run(t, "generate", "--config", at(directory, "ratel.yaml"))
	if result.code != 1 || !strings.Contains(result.stderr, "symbolic link") {
		t.Fatalf("generate = %d: %s", result.code, result.stderr)
	}

	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("wrote through the link: %v, %v", entries, err)
	}
}

func TestCheck(t *testing.T) {
	warmParser(t)

	t.Parallel()

	for _, tc := range []struct {
		name, queries string
		code          int
	}{
		{name: "valid", queries: userByID},
		{name: "unknown column", queries: "-- name: Broken :one\nSELECT missing FROM public.users;\n", code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			directory := generateFixture(t, tc.queries)

			if result := run(t, "check", "--config", at(directory, "ratel.yaml")); result.code != tc.code {
				t.Fatalf("check = %d, want %d: %s", result.code, tc.code, result.stderr)
			}
		})
	}
}

func BenchmarkGenerate(b *testing.B) {
	config := at(generateFixture(b, userByID), "ratel.yaml")

	for b.Loop() {
		if result := run(b, "generate", "--config", config); result.code != 0 {
			b.Fatal(result.stderr)
		}
	}
}
