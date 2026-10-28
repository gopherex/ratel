//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

const e2eConfig = `version: 1
postgres: {version: 18}
schema: [schema/*.sql]
queries:
  - files: [queries/*.sql]
    package: queries
plugins:
  - name: go
    builtin: true
    out: db
    options: {import_path: example.com/e2e/db}
`

// TestGeneratedCode runs ratel generate on a fixture, builds the generated
// packages in their own module and runs a program using them against real
// PostgreSQL: registration, inserts, reads, relations, static queries, COPY
// and constraint sentinels.
func TestGeneratedCode(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	module := t.TempDir()
	files := map[string]string{
		"ratel.yaml":      e2eConfig,
		"go.mod":          "module example.com/e2e\n\ngo 1.27.3\n",
		"doc/doc.go":      "package doc\n\n// Profile is a JSON document.\ntype Profile struct{ Name string }\n",
		"schema/app.sql":  read(t, "testdata/gengo/schema.sql"),
		"queries/app.sql": read(t, "testdata/gengo/queries.sql"),
		"e2e_test.go":     read(t, "testdata/gengo/program_test.go.txt"),
	}

	for name, contents := range files {
		write(t, filepath.Join(module, filepath.FromSlash(name)), contents)
	}

	var stdout, stderr bytes.Buffer
	if code := cli.Main([]string{"ratel", "generate", "--config", filepath.Join(module, "ratel.yaml")},
		&stdout, &stderr); code != 0 {
		t.Fatalf("generate = %d: %s", code, &stderr)
	}

	workspace := filepath.Join(t.TempDir(), "go.work")
	write(t, workspace, "go 1.27.3\n\nuse (\n\t"+module+"\n\t"+root+"\n)\n")

	pool := postgresPool(t, 18)

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	if _, err := pool.Exec(ctx, files["schema/app.sql"]); err != nil {
		t.Fatal(err)
	}

	environment := append(os.Environ(), "GOWORK="+workspace, "RATEL_E2E_DSN="+pool.Config().ConnString())

	goCommand(t, module, append(environment, "GOFLAGS=-mod=mod"), "get", "github.com/jackc/pgx/v5@v5.11.0",
		"github.com/shopspring/decimal", "github.com/jackc/pgx-shopspring-decimal")
	goCommand(t, module, environment, "vet", "./...")
	goCommand(t, module, environment, "test", "-count=1", "./...")
}

func goCommand(t *testing.T, directory string, environment []string, args ...string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir, command.Env = directory, environment

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, output)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func write(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
