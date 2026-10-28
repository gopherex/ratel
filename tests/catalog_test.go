//go:build integration

package tests_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestCatalogFixtureApply(t *testing.T) {
	t.Parallel()

	for _, image := range []string{"postgres:16-alpine", "postgres:17-alpine", "postgres:18-alpine"} {
		t.Run(image, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), containerWait)
			defer cancel()

			container, err := postgres.Run(ctx, image, testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2).
					WithStartupTimeout(containerWait),
			))
			if err != nil {
				t.Fatalf("start PostgreSQL: %v", err)
			}

			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), containerWait)
				defer cleanupCancel()

				if err := container.Terminate(cleanupCtx); err != nil {
					t.Errorf("terminate PostgreSQL: %v", err)
				}
			})

			pool, err := pgxpool.New(ctx, container.MustConnectionString(ctx))
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()

			ddl, err := os.ReadFile("testdata/catalog/schema.sql")
			if err != nil {
				t.Fatal(err)
			}

			if _, err := pool.Exec(ctx, string(ddl)); err != nil {
				t.Fatalf("apply fixture: %v", err)
			}

			assertCatalogFixture(ctx, t, pool)
		})
	}
}

func assertCatalogFixture(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"tables", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='catalog_fixture' AND c.relkind IN ('r','p')`, 4},
		{"foreign keys", `SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace
   WHERE n.nspname='catalog_fixture' AND c.contype='f'`, 2},
		{"RLS", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='catalog_fixture' AND c.relrowsecurity AND c.relforcerowsecurity`, 1},
		{"table comment", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='catalog_fixture' AND obj_description(c.oid,'pg_class')='Parent records'`, 1},
	} {
		var got int
		if err := pool.QueryRow(ctx, tc.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
