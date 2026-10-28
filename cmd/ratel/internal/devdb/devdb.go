// Package devdb provides the disposable dev PostgreSQL that normalizes both
// sides of a migration diff and runs rewrite probes: a testcontainers server
// from dev.image or an existing server from dev.url. Each use gets its own
// database, created from template0 and dropped afterwards.
package devdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

const (
	// readyLog is logged twice by the postgres image: after initialization
	// and after the restart that makes it reachable.
	readyLog         = "database system is ready to accept connections"
	readyOccurrences = 2
	// nameBytes of randomness name a dev database.
	nameBytes = 8
)

// errNoServer means neither dev.image nor dev.url is configured.
var errNoServer = errors.New("dev: neither image nor url is configured")

// Server is a dev PostgreSQL server; Close stops a container it started.
type Server struct {
	admin     *pgxpool.Pool
	address   *url.URL
	container testcontainers.Container
}

// Start connects to dev.url or starts a container from dev.image. The
// context bounds the startup.
func Start(ctx context.Context, settings config.Dev) (*Server, error) {
	address := settings.URL

	var container testcontainers.Container

	if address == "" {
		if settings.Image == "" {
			return nil, errNoServer
		}

		started, err := postgres.Run(ctx, settings.Image,
			testcontainers.WithWaitStrategy(wait.ForLog(readyLog).WithOccurrence(readyOccurrences)))
		if err != nil {
			return nil, fmt.Errorf("dev: start %s: %w", settings.Image, err)
		}

		container = started

		address, err = started.ConnectionString(ctx)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("dev: address: %w", err), started.Terminate(context.WithoutCancel(ctx)))
		}
	}

	parsed, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("dev: url: %w", err)
	}

	admin, err := pgxpool.New(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("dev: connect: %w", err)
	}

	return &Server{admin: admin, address: parsed, container: container}, nil
}

// Major is the server's PostgreSQL major version.
func (s *Server) Major(ctx context.Context) (int, error) {
	var major int

	err := s.admin.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&major)
	if err != nil {
		return 0, fmt.Errorf("dev: version: %w", err)
	}

	return major, nil
}

// Database is a fresh database on the dev server.
type Database struct {
	Pool   *pgxpool.Pool
	name   string
	server *Server
}

// Database creates an empty database from template0.
func (s *Server) Database(ctx context.Context) (*Database, error) {
	random := make([]byte, nameBytes)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("dev: name: %w", err)
	}

	name := "ratel_" + hex.EncodeToString(random)

	if _, err := s.admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0"); err != nil {
		return nil, fmt.Errorf("dev: create database: %w", err)
	}

	address := *s.address
	address.Path = "/" + name

	pool, err := pgxpool.New(ctx, address.String())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("dev: connect %s: %w", name, err), s.drop(ctx, name))
	}

	return &Database{Pool: pool, name: name, server: s}, nil
}

// Drop closes the database's connections and drops it.
func (d *Database) Drop(ctx context.Context) error {
	d.Pool.Close()

	return d.server.drop(ctx, d.name)
}

func (s *Server) drop(ctx context.Context, name string) error {
	statement := "DROP DATABASE IF EXISTS " + pgx.Identifier{name}.Sanitize() + " WITH (FORCE)"
	if _, err := s.admin.Exec(ctx, statement); err != nil {
		return fmt.Errorf("dev: drop database: %w", err)
	}

	return nil
}

// Close releases the server and stops a container Start created.
func (s *Server) Close(ctx context.Context) error {
	s.admin.Close()

	if s.container == nil {
		return nil
	}

	if err := s.container.Terminate(ctx); err != nil {
		return fmt.Errorf("dev: stop container: %w", err)
	}

	return nil
}
