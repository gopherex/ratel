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

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
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
		snapshots, err := generate(ctx, target.image, timeout)
		if err != nil {
			return fmt.Errorf("PostgreSQL %d: %w", target.major, err)
		}

		for suffix, data := range map[string][]byte{"": snapshots.core, "-extensions": snapshots.extensions} {
			path := filepath.Join(output, strconv.Itoa(target.major)+suffix+".binpb")
			if err := os.WriteFile(path, data, fileMode); err != nil {
				return fmt.Errorf("write snapshot: %w", err)
			}
		}

		slog.Info("generated builtin snapshot", "major", target.major,
			"bytes", len(snapshots.core), "extension_bytes", len(snapshots.extensions))
	}

	return nil
}

// snapshots are the encoded signatures of one PostgreSQL image: pg_catalog,
// and its contrib extensions.
type snapshots struct{ core, extensions []byte }

func generate(ctx context.Context, image string, timeout time.Duration) (snapshots, error) {
	const readyOccurrences = 2

	container, startErr := postgres.Run(ctx, image,
		postgres.WithDatabase("builtins"), postgres.WithUsername("builtins"), postgres.WithPassword("builtins"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(readyOccurrences).WithStartupTimeout(timeout)))
	if container == nil {
		return snapshots{}, fmt.Errorf("start PostgreSQL: %w", startErr)
	}

	var data snapshots

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

func extract(ctx context.Context, container *postgres.PostgresContainer, timeout time.Duration) (snapshots, error) {
	address, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return snapshots{}, fmt.Errorf("PostgreSQL address: %w", err)
	}

	connection, err := pgx.Connect(ctx, address)
	if err != nil {
		return snapshots{}, fmt.Errorf("connect PostgreSQL: %w", err)
	}

	result, readErr := read(ctx, connection)

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	if err := errors.Join(readErr, connection.Close(cleanup)); err != nil {
		return snapshots{}, err
	}

	return result, nil
}

func read(ctx context.Context, connection *pgx.Conn) (snapshots, error) {
	core, err := signatures(ctx, connection, "pg_catalog", "pg_catalog")
	if err != nil {
		return snapshots{}, err
	}

	encodedCore, err := snapshot.Encode(core)
	if err != nil {
		return snapshots{}, fmt.Errorf("encode pg_catalog: %w", err)
	}

	extensions, err := extensionSignatures(ctx, connection)
	if err != nil {
		return snapshots{}, err
	}

	encodedExtensions, err := snapshot.EncodeExtensions(extensions)
	if err != nil {
		return snapshots{}, fmt.Errorf("encode extensions: %w", err)
	}

	return snapshots{core: encodedCore, extensions: encodedExtensions}, nil
}

// extensionSignatures installs each available extension alone into
// builtins.ExtensionSchema, its requirements into a schema of their own, and
// reads what it created; the transaction is rolled back after each. An
// extension that cannot be installed this way is skipped and logged.
func extensionSignatures(ctx context.Context, connection *pgx.Conn) ([]*ir.Schema, error) {
	rows, err := connection.Query(ctx, `SELECT e.name, COALESCE(v.requires, '{}')::text[]
FROM pg_catalog.pg_available_extensions e
JOIN pg_catalog.pg_available_extension_versions v ON v.name = e.name AND v.version = e.default_version
WHERE e.installed_version IS NULL ORDER BY e.name`)
	if err != nil {
		return nil, fmt.Errorf("list extensions: %w", err)
	}

	type available struct {
		name     string
		requires []string
	}

	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (available, error) {
		var item available

		return item, row.Scan(&item.name, &item.requires)
	})
	if err != nil {
		return nil, fmt.Errorf("list extensions: %w", err)
	}

	var result []*ir.Schema

	for _, item := range list {
		schema, err := installed(ctx, connection, item.name, item.requires)
		if err != nil {
			slog.Warn("skip extension", "extension", item.name, "error", err)

			continue
		}

		schema.Name = item.name
		result = append(result, schema)
	}

	return result, nil
}

func installed(ctx context.Context, connection *pgx.Conn, name string, requires []string) (*ir.Schema, error) {
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}

	defer func() { _ = transaction.Rollback(context.WithoutCancel(ctx)) }()

	statements := []string{
		"CREATE SCHEMA " + builtins.ExtensionSchema, "CREATE SCHEMA " + dependencySchema,
	}
	for _, requirement := range requires {
		statements = append(statements, "CREATE EXTENSION IF NOT EXISTS "+pgx.Identifier{requirement}.Sanitize()+
			" SCHEMA "+dependencySchema+" CASCADE")
	}

	statements = append(statements,
		"CREATE EXTENSION "+pgx.Identifier{name}.Sanitize()+" SCHEMA "+builtins.ExtensionSchema)

	for _, statement := range statements {
		if _, err := transaction.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("install: %w", err)
		}
	}

	return signatures(ctx, transaction, builtins.ExtensionSchema, "pg_catalog", builtins.ExtensionSchema, dependencySchema)
}

// dependencySchema holds the extensions an extracted extension requires, so
// their objects stay out of its snapshot.
const dependencySchema = "ratel_deps"

// querier runs catalogSQL on a connection or in a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// signatures reads the objects of schema; their argument and result types may
// come from any of visible.
func signatures(ctx context.Context, connection querier, schema string, visible ...string) (*ir.Schema, error) {
	var data []byte
	if err := connection.QueryRow(ctx, catalogSQL, schema, visible).Scan(&data); err != nil {
		return nil, fmt.Errorf("read catalog %s: %w", schema, err)
	}

	result := &ir.Schema{}
	if err := protojson.Unmarshal(data, result); err != nil {
		return nil, fmt.Errorf("decode catalog %s: %w", schema, err)
	}

	return result, nil
}
