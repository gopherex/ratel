package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

func TestMigrateUsage(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"plan"},
		{"plan", "a", "b"},
		{"new"},
		{"new", "a", "b"},
		{"hash", "a"},
		{"apply"},
		{"verify", "--url", "postgres://x"},
		{"status", "extra", "--url", "postgres://x"},
		{"verify", "--unknown"},
		{"verify", "--log-level", "loud"},
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

// TestMigrateNewHash creates a hand-written migration, edits it and seals
// it: verify passes before the edit and after hash, not in between.
func TestMigrateNewHash(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := filepath.Join(root, "ratel.yaml")

	writeFile(t, config, "version: 1\nschema: []\n")

	// run is the exit code and the output, as "<code>: <output>".
	run := func(args ...string) string {
		var output bytes.Buffer

		code := cli.Main(append([]string{"ratel", "migrate", args[0], "--config", config}, args[1:]...), &output, &output)

		return strconv.Itoa(code) + ": " + output.String()
	}

	created := run("new", "add_index")

	name, ok := strings.CutPrefix(strings.TrimSpace(created), "0: wrote ")
	if !ok || !strings.HasSuffix(name, "_add_index.sql") {
		t.Fatalf("new = %s", created)
	}

	if verify := run("verify"); verify != "0: " {
		t.Fatalf("verify of a new file = %s", verify)
	}

	path := filepath.Join(root, "migrations", name)

	template, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, path, string(template)+"CREATE INDEX CONCURRENTLY i ON app.t (a);\n")

	if verify := run("verify"); !strings.HasPrefix(verify, "1: ") || !strings.Contains(verify, "ratel.sum") {
		t.Fatalf("verify of an edited file = %s", verify)
	}

	if hash := run("hash"); hash != "0: sealed "+name+"\n" {
		t.Fatalf("hash = %s", hash)
	}

	if verify := run("verify"); verify != "0: " {
		t.Fatalf("verify of a sealed file = %s", verify)
	}

	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(sealed), "tx=own class=long by=hand locks=app.t:ShareUpdateExclusive") {
		t.Fatalf("sealed file:\n%s", sealed)
	}

	if hash := run("hash"); hash != "0: " {
		t.Fatalf("second hash = %s", hash)
	}
}

// TestMigrateFlagsAfterOperands accepts flags after the operands, as in
// "ratel migrate new add_index --config ratel.yaml".
func TestMigrateFlagsAfterOperands(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := filepath.Join(root, "ratel.yaml")

	writeFile(t, config, "version: 1\nschema: []\n")

	var output bytes.Buffer

	code := cli.Main([]string{"ratel", "migrate", "new", "late", "--config", config, "--log-level", "warn"},
		&output, &output)
	if code != 0 || !strings.Contains(output.String(), "_late.sql") {
		t.Fatalf("new with trailing flags = %d: %s", code, output.String())
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
