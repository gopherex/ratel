//go:build integration

package tests_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
)

// TestDevDatabase creates two isolated databases on a dev server reached
// through dev.url and drops them.
func TestDevDatabase(t *testing.T) {
	t.Parallel()

	pool := postgresPool(t, 18)

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	server, err := devdb.Start(ctx, config.Dev{URL: pool.Config().ConnString()}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close(context.WithoutCancel(ctx)) }()

	if major, err := server.Major(ctx); err != nil || major != 18 {
		t.Fatalf("Major() = %d, %v", major, err)
	}

	first, err := server.Database(ctx)
	if err != nil {
		t.Fatal(err)
	}

	second, err := server.Database(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := first.Pool.Exec(ctx, `CREATE TABLE public.only_here (id int)`); err != nil {
		t.Fatal(err)
	}

	var visible bool
	if err := second.Pool.QueryRow(ctx, `SELECT to_regclass('public.only_here') IS NOT NULL`).Scan(&visible); err != nil ||
		visible {
		t.Fatalf("databases share objects: %v, %v", visible, err)
	}

	for _, database := range []*devdb.Database{first, second} {
		if err := database.Drop(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var left int

	err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname LIKE 'ratel\_%'`).Scan(&left)
	if err != nil || left != 0 {
		t.Fatalf("databases left: %d, %v", left, err)
	}
}
