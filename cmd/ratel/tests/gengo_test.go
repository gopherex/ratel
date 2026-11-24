//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
// PostgreSQL 16 through 19: registration, inserts, reads, relations, static
// queries, COPY, constraint sentinels and computed columns.
func TestGeneratedCode(t *testing.T) {
	t.Parallel()

	lazyConfig := strings.Replace(e2eConfig, "import_path:", "lazy_descriptors: true, import_path:", 1)

	for _, tc := range []struct{ name, config, program string }{
		{"variables", e2eConfig, "program_test.go.txt"},
		{"accessors", lazyConfig, "accessors_test.go.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testGeneratedCode(t, tc.config, tc.program)
		})
	}
}

func testGeneratedCode(t *testing.T, config, program string) {
	t.Helper()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	module := t.TempDir()
	files := map[string]string{
		"ratel.yaml":      config,
		"go.mod":          "module example.com/e2e\n\ngo 1.27.3\n",
		"doc/doc.go":      "package doc\n\n// Profile is a JSON document.\ntype Profile struct{ Name string }\n",
		"schema/app.sql":  read(t, "testdata/gengo/schema.sql"),
		"queries/app.sql": read(t, "testdata/gengo/queries.sql"),
		"e2e_test.go":     read(t, filepath.Join("testdata", "gengo", program)),
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

	environment := append(os.Environ(), "GOWORK="+workspace)

	goCommand(t, module, append(environment, "GOFLAGS=-mod=mod"), "get", "github.com/jackc/pgx/v5@v5.11.0",
		"github.com/shopspring/decimal", "github.com/jackc/pgx-shopspring-decimal")
	goCommand(t, module, environment, "vet", "./...")

	if program == "accessors_test.go.txt" {
		testDescriptorLinking(t, module, environment)
	}

	for _, major := range []int{16, 17, 18, 19} {
		t.Run(strconv.Itoa(major), func(t *testing.T) {
			t.Parallel()

			pool := postgresPool(t, major)

			ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
			defer cancel()

			if _, err := pool.Exec(ctx, files["schema/app.sql"]); err != nil {
				t.Fatal(err)
			}

			goCommand(t, module, append(slices.Clone(environment), "RATEL_E2E_DSN="+pool.Config().ConnString()),
				"test", "-count=1", "./...")
		})
	}
}

// A process that selects users must retain their scanner and drop the unrelated tags table,
// even though both tables belong to the same generated Go package.
func testDescriptorLinking(t *testing.T, module string, environment []string) {
	t.Helper()
	write(t, filepath.Join(module, "probe", "main.go"), `package main

import (
    "fmt"
    "example.com/e2e/db/app"
)

func main() { fmt.Println(app.Users().Select().Build()) }
`)

	binary := filepath.Join(t.TempDir(), "probe")
	goCommand(t, module, environment, "build", "-ldflags=", "-o", binary, "./probe")

	symbols := goCommand(t, module, environment, "tool", "nm", binary)
	if !bytes.Contains(symbols, []byte("example.com/e2e/db/app.scanUser")) ||
		bytes.Contains(symbols, []byte("example.com/e2e/db/app.scanTag")) {
		t.Fatal("expected the users scanner alone of users and tags in the executable")
	}
}

func goCommand(t *testing.T, directory string, environment []string, args ...string) []byte {
	t.Helper()

	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir, command.Env = directory, environment

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, output)
	}

	return output
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
