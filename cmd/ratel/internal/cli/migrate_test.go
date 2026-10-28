package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

func TestMigrateUsage(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{},
		{"plan"},
		{"plan", "a", "b"},
		{"apply"},
		{"verify", "--url", "postgres://x"},
		{"status", "extra", "--url", "postgres://x"},
		{"verify", "--unknown"},
		{"rollback"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer

			code := cli.Main(append([]string{"ratel", "migrate"}, args...), &bytes.Buffer{}, &stderr)
			if code != 2 {
				t.Errorf("exit %d:\n%s", code, stderr.String())
			}
		})
	}
}

func TestMigrateVerify(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := filepath.Join(root, "ratel.yaml")

	writeFile(t, config, "version: 1\nschema: []\n")

	run := func() (int, string) {
		var stderr bytes.Buffer

		code := cli.Main([]string{"ratel", "migrate", "verify", "--config", config}, &bytes.Buffer{}, &stderr)

		return code, stderr.String()
	}

	if code, output := run(); code != 0 {
		t.Fatalf("empty directory = %d: %s", code, output)
	}

	writeFile(t, filepath.Join(root, "migrations", "1_a.sql"), "SELECT 1;")

	if code, output := run(); code != 1 || !strings.Contains(output, "ratel.sum") {
		t.Fatalf("file without ratel.sum = %d: %s", code, output)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
