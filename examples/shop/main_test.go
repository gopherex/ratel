//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const containerWait = 5 * time.Minute

// TestShop runs the quick start against PostgreSQL: migrations applied on
// startup, writes, an eager relation and a static query.
func TestShop(t *testing.T) {
	t.Parallel()

	image := os.Getenv("RATEL_PG_IMAGE")
	if image == "" {
		image = "postgres:18-alpine"
	}

	ctx, cancel := context.WithTimeout(t.Context(), containerWait)
	defer cancel()

	container, err := postgres.Run(ctx, image, testcontainers.WithWaitStrategy(
		wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(containerWait),
	))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.WithoutCancel(ctx)) })

	var out bytes.Buffer
	if err := run(ctx, container.MustConnectionString(ctx, "sslmode=disable"), &out); err != nil {
		t.Fatal(err)
	}

	if want := "Ann has 2 orders\nann@example.com 52.5\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

// TestSourcesUpToDate regenerates the code from the sources and verifies the
// migrations, so the committed example is what ratel produces from it.
func TestSourcesUpToDate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binary := filepath.Join(root, "ratel")

	command(t, ".", "go", "build", "-o", binary, "github.com/gopherex/ratel/cmd/ratel")

	clone := filepath.Join(root, "shop")

	for _, path := range []string{"ratel.yaml", "schema/schema.sql", "queries/users/users.sql"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		target := filepath.Join(clone, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	command(t, ".", binary, "generate", "--config", filepath.Join(clone, "ratel.yaml"))

	for _, path := range []string{"internal/db/app/app.go", "internal/db/users/users.go"} {
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		generated, err := os.ReadFile(filepath.Join(clone, path))
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(committed, generated) {
			t.Errorf("%s differs from ratel generate; regenerate the example", path)
		}
	}

	command(t, ".", binary, "migrate", "verify")
}

func command(t *testing.T, dir, name string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
}
