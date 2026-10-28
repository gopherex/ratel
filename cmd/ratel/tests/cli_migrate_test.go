//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

const schemaV1 = `CREATE SCHEMA app;
CREATE TABLE app.users (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, email text NOT NULL);
CREATE TABLE app.orders (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES app.users,
  total numeric(12, 2) NOT NULL
);
CREATE INDEX orders_user_id ON app.orders (user_id);`

const schemaV2 = `CREATE SCHEMA app;
CREATE TABLE app.users (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email text NOT NULL,
  -- @ratel.backfill expr='active'
  status text NOT NULL
);
CREATE TABLE app.orders (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES app.users,
  total numeric(12, 2) NOT NULL CHECK (total >= 0)
);
CREATE INDEX orders_user_id ON app.orders (user_id);
CREATE INDEX users_email ON app.users (lower(email));`

// project is a temporary ratel project on the dev server.
type project struct {
	t    *testing.T
	root string
}

func newProject(t *testing.T, devURL string) project {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(root, "ratel.yaml"), "version: 1\npostgres: {version: 18}\nschema: [schema/*.sql]\n"+
		"dev: {url: '"+devURL+"'}\n")

	return project{t: t, root: root}
}

func (p project) schema(sql string) { write(p.t, filepath.Join(p.root, "schema", "app.sql"), sql) }

// ratel runs a migrate subcommand and returns its exit code and output.
func (p project) ratel(args ...string) (int, string) {
	p.t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Main(append([]string{"ratel", "migrate", args[0], "--config", filepath.Join(p.root, "ratel.yaml")},
		args[1:]...), &stdout, &stderr)

	return code, stdout.String() + stderr.String()
}

func (p project) must(args ...string) string {
	p.t.Helper()

	code, output := p.ratel(args...)
	if code != 0 {
		p.t.Fatalf("ratel migrate %s = %d:\n%s", strings.Join(args, " "), code, output)
	}

	return output
}

func TestMigrateCommands(t *testing.T) {
	t.Parallel()

	server := postgresPool(t, 18)

	ctx, cancel := context.WithTimeout(t.Context(), 3*startupTimeout)
	defer cancel()

	if _, err := server.Exec(ctx, `CREATE DATABASE target`); err != nil {
		t.Fatal(err)
	}

	settings := server.Config().ConnConfig.Copy()
	settings.Database = "target"
	url := settings.ConnString()

	target, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}

	defer target.Close()

	p := newProject(t, server.Config().ConnString())

	p.schema(schemaV1)
	p.must("plan", "init")
	p.must("verify")
	p.must("lint")
	p.must("apply", "--url", url)
	p.must("contract", "--url", url)

	if output := p.must("status", "--url", url); strings.Contains(output, "pending") {
		t.Fatalf("status after apply:\n%s", output)
	}

	if output := p.must("plan", "again"); !strings.Contains(output, "no changes") {
		t.Fatalf("second plan:\n%s", output)
	}

	_, err = target.Exec(ctx, `INSERT INTO app.users (email) VALUES ('a@x'), ('b@x');
		INSERT INTO app.orders (user_id, total) SELECT id, 10 FROM app.users`)
	if err != nil {
		t.Fatal(err)
	}

	p.schema(schemaV2)

	output := p.must("plan", "status")
	for _, want := range []string{"fixed RTL-L02", "fixed RTL-L04", "fixed RTL-C03"} {
		if !strings.Contains(output, want) {
			t.Errorf("plan output lacks %q:\n%s", want, output)
		}
	}

	p.must("apply", "--url", url)
	p.must("contract", "--url", url)
	p.must("drift", "--url", url)

	var filled bool
	if err := target.QueryRow(ctx, `SELECT bool_and(status = 'active') FROM app.users`).Scan(&filled); err != nil ||
		!filled {
		t.Fatalf("backfilled status: %v, %v", filled, err)
	}

	if _, err := target.Exec(ctx, `ALTER TABLE app.users ADD COLUMN extra int`); err != nil {
		t.Fatal(err)
	}

	if code, output := p.ratel("drift", "--url", url); code != 1 || !strings.Contains(output, "column app.users.extra") {
		t.Fatalf("drift = %d:\n%s", code, output)
	}

	checkTamper(t, p)
}

// checkTamper edits a planned file and adds a hand-written one breaking a
// rule: verify fails on the first, lint on the second.
func checkTamper(t *testing.T, p project) {
	t.Helper()

	hand := filepath.Join(p.root, "migrations", "99999999999999_hand.sql")
	write(t, hand, "CREATE INDEX orders_total ON app.orders (total);\n")

	if code, output := p.ratel("lint"); code != 1 || !strings.Contains(output, "error RTL-L02") {
		t.Fatalf("lint of a blocking index = %d:\n%s", code, output)
	}

	if code, _ := p.ratel("verify"); code != 1 {
		t.Fatal("verify accepted a file missing from ratel.sum")
	}
}
