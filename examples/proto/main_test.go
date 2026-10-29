//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/gopherex/ratel"

	shopv1 "github.com/gopherex/ratel/examples/proto/gen/shop/v1"
	"github.com/gopherex/ratel/examples/proto/internal/db/shop"
)

const containerWait = 5 * time.Minute

// server starts PostgreSQL 18, which the example's migrations are planned for.
func server(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), containerWait)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:18-alpine", testcontainers.WithWaitStrategy(
		wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(containerWait),
	))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.WithoutCancel(ctx)) })

	return container.MustConnectionString(ctx, "sslmode=disable")
}

// TestProto runs the quick start: migrations, inserts from messages, a read
// with a proto condition and a relation, Save and Pluck.
func TestProto(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(t.Context(), server(t), &out); err != nil {
		t.Fatal(err)
	}

	if want := "ann@example.com Riga +371 2000 0000 100 1\nSTATUS_CLOSED\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

// TestRoundTrip writes a message with every conversion and reads it back.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	url := server(t)
	if err := run(t.Context(), url, io.Discard); err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	database := ratel.New(pool)

	meta, err := structpb.NewStruct(map[string]any{"vip": true, "rank": 3.5})
	if err != nil {
		t.Fatal(err)
	}

	nickname := "bob"
	bob := &shopv1.Customer{
		Id: 2, TenantNo: 4_000_000_000, Email: "bob@example.com", Nickname: &nickname,
		Status:    shopv1.Status_STATUS_CLOSED,
		CreatedAt: timestamppb.New(time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC)),
		Ttl:       durationpb.New(90*time.Minute + 1500*time.Microsecond),
		Score:     wrapperspb.Int32(-4),
		Tags:      []string{"a", "b"},
		Counters:  map[string]int32{"x": 1},
		Address:   &shopv1.Address{City: "Tallinn"},
		Balance:   18_000_000_000_000_000_000,
		Meta:      meta,
		Notes:     []*shopv1.Note{{Text: "first"}, {Text: "second"}},
		Pinned:    map[string]*shopv1.Note{"top": {Text: "pinned"}},
		Subtotal:  21,
		Contact:   &shopv1.Customer_Fax{Fax: "fax-1"},
	}

	if _, err := database.Run(shop.Customers.Insert(shop.Customers.RowOf(bob))).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}

	read, err := database.Run(shop.Customers.Select().Where(shop.Customers.ID.Eq(2))).One(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := proto.CloneOf(bob)
	want.Total = 42 // generated: subtotal * 2

	if !proto.Equal(read.Customer, want) {
		t.Fatalf("read back\n%v\nwant\n%v", read.Customer, want)
	}

	// A message without the embedded message and oneof writes NULL to them.
	empty := &shopv1.Customer{Id: 3, Email: "c@example.com"}
	if _, err := database.Run(shop.Customers.Insert(shop.Customers.RowOf(empty))).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}

	read, err = database.Run(shop.Customers.Select().Where(shop.Customers.ID.Eq(3))).One(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if read.GetAddress() != nil || read.GetContact() != nil || read.GetStatus() != shopv1.Status_STATUS_ACTIVE ||
		read.GetCreatedAt() == nil {
		t.Fatalf("defaults and absent messages: %v", read.Customer)
	}
}

// TestSourcesUpToDate regenerates the code from the schema and verifies the
// migrations, so the committed example is what ratel produces from it.
func TestSourcesUpToDate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binary := filepath.Join(root, "ratel")

	command(t, ".", "go", "build", "-o", binary, "github.com/gopherex/ratel/cmd/ratel")

	clone := filepath.Join(root, "proto")

	for _, path := range []string{"ratel.yaml", "schema/schema.sql", "schema/shop/v1/shop.sql"} {
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

	path := "internal/db/shop/shop.go"

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
