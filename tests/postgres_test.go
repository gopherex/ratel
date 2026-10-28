//go:build integration

package tests_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Defaults when the environment overrides nothing.
const (
	defaultImage  = "postgres:18-alpine"
	containerWait = 5 * time.Minute
	majorDivisor  = 10000
)

func TestPostgresImageVersion(t *testing.T) {
	t.Parallel()

	image := os.Getenv("RATEL_PG_IMAGE")
	if image == "" {
		image = defaultImage
	}

	ctx, cancel := context.WithTimeout(context.Background(), containerWait)
	defer cancel()

	ctr, err := postgres.Run(
		ctx,
		image,
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(containerWait),
		),
	)
	if err != nil {
		t.Fatalf("start container from %q: %v", image, err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), containerWait)
		defer cleanupCancel()

		if err := ctr.Terminate(cleanupCtx); err != nil {
			t.Logf("terminate container from %q: %v", image, err)
		}
	})

	pool, err := pgxpool.New(ctx, ctr.MustConnectionString(ctx))
	if err != nil {
		t.Fatalf("connect to %q: %v", image, err)
	}

	defer pool.Close()

	// server_version_num is text on the wire: a six-digit number such as 180002.
	var serverVersionNum string
	if err := pool.QueryRow(ctx, "SHOW server_version_num").Scan(&serverVersionNum); err != nil {
		t.Fatalf("query server_version_num: %v", err)
	}

	num, err := strconv.Atoi(serverVersionNum)
	if err != nil {
		t.Fatalf("parse server_version_num %q: %v", serverVersionNum, err)
	}

	gotMajor := num / majorDivisor
	wantMajor := imageMajor(t, image)

	if gotMajor != wantMajor {
		t.Errorf("image %q runs PostgreSQL major %d, want %d", image, gotMajor, wantMajor)
	}
}

// imageMajor extracts the major version from an image reference such as
// postgres:18-alpine.
func imageMajor(t *testing.T, image string) int {
	t.Helper()

	_, tag, _ := strings.Cut(image, ":")

	end := strings.IndexFunc(tag, func(r rune) bool { return r < '0' || r > '9' })
	if end == -1 {
		end = len(tag)
	}

	major, err := strconv.Atoi(tag[:end])
	if err != nil {
		t.Fatalf("parse major version from image %q: %v", image, err)
	}

	return major
}
