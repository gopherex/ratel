// Command gen extracts inference signatures from pristine PostgreSQL containers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins/snapshot"

	_ "embed"
)

//go:embed catalog.sql
var catalogSQL string

func main() {
	const defaultTimeout = 10 * time.Minute

	output := flag.String("out", "data", "snapshot directory")
	timeout := flag.Duration("timeout", defaultTimeout, "generation and cleanup deadline")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	err := run(ctx, *output, *timeout)

	cancel()

	if err != nil {
		slog.Error("generate builtins", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output string, timeout time.Duration) error {
	const directoryMode, fileMode = 0o755, 0o644

	if err := os.MkdirAll(output, directoryMode); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}

	targets := []struct {
		major int
		image string
	}{
		{16, "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"},
		{17, "postgres:17-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b"},
		{18, "postgres:18-alpine@sha256:898c60cb390d1085bc97bdd6a1ae968757421df394b071848b52b1c09c89d6a5"},
	}
	for _, target := range targets {
		data, err := generate(ctx, target.image, timeout)
		if err != nil {
			return fmt.Errorf("PostgreSQL %d: %w", target.major, err)
		}

		path := filepath.Join(output, strconv.Itoa(target.major)+".binpb")
		if err := os.WriteFile(path, data, fileMode); err != nil {
			return fmt.Errorf("write snapshot: %w", err)
		}

		slog.Info("generated builtin snapshot", "major", target.major, "bytes", len(data))
	}

	return nil
}

func generate(ctx context.Context, image string, timeout time.Duration) ([]byte, error) {
	const readyOccurrences = 2

	container, startErr := postgres.Run(ctx, image,
		postgres.WithDatabase("builtins"), postgres.WithUsername("builtins"), postgres.WithPassword("builtins"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(readyOccurrences).WithStartupTimeout(timeout)))
	if container == nil {
		return nil, fmt.Errorf("start PostgreSQL: %w", startErr)
	}

	var data []byte

	operationErr := startErr
	if operationErr == nil {
		data, operationErr = extract(ctx, container, timeout)
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	if err := container.Terminate(cleanup); err != nil {
		operationErr = errors.Join(operationErr, fmt.Errorf("terminate PostgreSQL: %w", err))
	}

	return data, operationErr
}

func extract(ctx context.Context, container *postgres.PostgresContainer, timeout time.Duration) ([]byte, error) {
	address, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL address: %w", err)
	}

	connection, err := pgx.Connect(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}

	var data []byte

	readErr := connection.QueryRow(ctx, catalogSQL).Scan(&data)

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	closeErr := connection.Close(cleanup)
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}

	schema := &ir.Schema{}
	if err := protojson.Unmarshal(data, schema); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}

	return snapshot.Encode(schema)
}
