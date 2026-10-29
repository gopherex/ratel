package rateltest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gopherex/ratel/migrate"
)

// PostgresOption configures a suite's container, template and connection pools.
type PostgresOption func(*postgresSettings)

type postgresSettings struct {
	image      string
	timeout    time.Duration
	migrations []func(context.Context, *pgxpool.Pool) error
	configure  func(*pgxpool.Config)
}

// WithImage overrides RATEL_PG_IMAGE and the default postgres:18-alpine image.
func WithImage(image string) PostgresOption {
	return func(settings *postgresSettings) { settings.image = image }
}

// WithTimeout bounds startup, database setup and each cleanup. Default: five minutes.
func WithTimeout(timeout time.Duration) PostgresOption {
	return func(settings *postgresSettings) { settings.timeout = timeout }
}

// WithMigrations applies trusted SQL in expand, backfill, contract order to the
// template. Statements may contain multiple commands. Inputs are copied; repeated
// options append phases in option order. No migration planner is run implicitly.
func WithMigrations(expand, backfill, contract []string) PostgresOption {
	statements := append(append(append([]string{}, expand...), backfill...), contract...)

	return func(settings *postgresSettings) {
		settings.migrations = append(settings.migrations, func(ctx context.Context, template *pgxpool.Pool) error {
			for _, statement := range statements {
				if _, err := template.Exec(ctx, statement); err != nil {
					return fmt.Errorf("migration: %w", err)
				}
			}

			return nil
		})
	}
}

// WithMigrationsDir applies a migrations directory, as ratel migrate plan and
// hash write it, to the template with package migrate: every phase, under the
// guards and journal an application's own run uses. Pass the directory the
// application embeds so tests run on the schema production gets. Options go
// to migrate.Apply and migrate.Contract; repeated options apply in order.
func WithMigrationsDir(migrations fs.FS, options ...migrate.Option) PostgresOption {
	return func(settings *postgresSettings) {
		settings.migrations = append(settings.migrations, func(ctx context.Context, template *pgxpool.Pool) error {
			if err := migrate.Apply(ctx, template, migrations, options...); err != nil {
				return fmt.Errorf("migrate apply: %w", err)
			}

			if err := migrate.Contract(ctx, template, migrations, options...); err != nil {
				return fmt.Errorf("migrate contract: %w", err)
			}

			return nil
		})
	}
}

// WithPoolConfig configures each isolated pool before creation. Use AfterConnect
// for a generated RegisterTypes, when a package has one, and ConnConfig.Tracer
// for Recorder. Hooks run only on isolated databases, after migrations. Multiple
// options run in order. The callback must be safe for concurrent test setup.
func WithPoolConfig(configure func(*pgxpool.Config)) PostgresOption {
	return func(settings *postgresSettings) {
		previous := settings.configure
		settings.configure = func(config *pgxpool.Config) {
			previous(config)
			configure(config)
		}
	}
}

// Server owns a container and a migrated template. Create it in a parent test,
// then call Postgres in parallel subtests. Parent cleanup runs after its children;
// every child owns an independent database. No process-global server is retained.
type Server struct {
	admin    *pgxpool.Pool
	settings postgresSettings
	template string
}

// NewServer starts a suite-owned PostgreSQL and prepares its template database.
func NewServer(tb testing.TB, options ...PostgresOption) *Server {
	tb.Helper()

	const (
		defaultTimeout = 5 * time.Minute
		readyMessages  = 2
	)

	settings := postgresSettings{
		image: os.Getenv("RATEL_PG_IMAGE"), timeout: defaultTimeout,
		configure: func(*pgxpool.Config) {},
	}
	if settings.image == "" {
		settings.image = "postgres:18-alpine"
	}

	for _, option := range options {
		option(&settings)
	}

	if settings.image == "" || settings.timeout <= 0 {
		tb.Fatal("rateltest: image and positive timeout are required")
	}

	ctx, cancel := context.WithTimeout(tb.Context(), settings.timeout)
	defer cancel()

	container, err := postgres.Run(ctx, settings.image,
		postgres.WithDatabase("postgres"), postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(readyMessages).WithStartupTimeout(settings.timeout)))
	if container != nil {
		tb.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(tb.Context()), settings.timeout)
			defer cleanupCancel()

			if err := container.Terminate(cleanupCtx); err != nil {
				tb.Errorf("rateltest: terminate container: %v", err)
			}
		})
	}

	if err != nil {
		tb.Fatalf("rateltest: start PostgreSQL: %v", err)
	}

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		tb.Fatalf("rateltest: connection string: %v", err)
	}

	admin, err := pgxpool.New(ctx, connectionString)
	if err != nil {
		tb.Fatalf("rateltest: admin pool: %v", err)
	}

	tb.Cleanup(admin.Close)

	server := &Server{admin: admin, settings: settings, template: databaseName()}
	template := server.database(tb, server.template, "template0", false)

	for _, apply := range settings.migrations {
		if err := apply(ctx, template); err != nil {
			tb.Fatalf("rateltest: apply template migration: %v", err)
		}
	}

	template.Close()

	return server
}

// Postgres starts a private server and returns an isolated pool. For a shared
// container, construct NewServer once in the parent test and call Server.Postgres.
func Postgres(tb testing.TB, options ...PostgresOption) *pgxpool.Pool {
	tb.Helper()

	return NewServer(tb, options...).Postgres(tb)
}

// Postgres clones the migrated template and wires a fresh pool. Cleanup closes
// the pool and drops the database, terminating connections opened by the test.
func (server *Server) Postgres(tb testing.TB) *pgxpool.Pool {
	tb.Helper()

	return server.database(tb, databaseName(), server.template, true)
}

func databaseName() string { return "rateltest_" + uuid.NewString() }

func (server *Server) database(tb testing.TB, name, template string, configure bool) *pgxpool.Pool {
	tb.Helper()

	ctx, cancel := context.WithTimeout(tb.Context(), server.settings.timeout)
	defer cancel()

	if _, err := server.admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+
		" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		tb.Fatalf("rateltest: clone database: %v", err)
	}

	tb.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(tb.Context()), server.settings.timeout)
		defer cleanupCancel()

		statement := "DROP DATABASE " + pgx.Identifier{name}.Sanitize() + " WITH (FORCE)"
		if _, err := server.admin.Exec(cleanupCtx, statement); err != nil {
			tb.Errorf("rateltest: drop database: %v", err)
		}
	})

	config := server.admin.Config()
	config.ConnConfig.Database = name

	if configure {
		server.settings.configure(config)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		tb.Fatalf("rateltest: isolated pool: %v", err)
	}

	tb.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		tb.Fatalf("rateltest: connect isolated pool: %v", err)
	}

	return pool
}
