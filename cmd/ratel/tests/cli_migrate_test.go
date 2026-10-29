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
CREATE TYPE app.state AS ENUM ('new', 'done', E'it''s \\ odd');
CREATE TABLE app.users (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, email text NOT NULL);
CREATE TABLE app.orders (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES app.users,
  total numeric(12, 2) NOT NULL
);
CREATE INDEX orders_user_id ON app.orders (user_id);`

const schemaV2 = `CREATE SCHEMA app;
-- @ratel.renamed from=done value=finished
-- @ratel.renamed from="it's \ odd" value="it's \ fine"
CREATE TYPE app.state AS ENUM ('new', 'finished', E'it''s \\ fine');
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

// run is the exit code and the output of a migrate subcommand.
type run struct {
	code   int
	output string
}

// ratel runs a migrate subcommand.
func (p project) ratel(args ...string) run {
	p.t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Main(append([]string{"ratel", "migrate", args[0], "--config", filepath.Join(p.root, "ratel.yaml")},
		args[1:]...), &stdout, &stderr)

	return run{code: code, output: stdout.String() + stderr.String()}
}

func (p project) must(args ...string) string {
	p.t.Helper()

	result := p.ratel(args...)
	if result.code != 0 {
		p.t.Fatalf("ratel migrate %s = %d:\n%s", strings.Join(args, " "), result.code, result.output)
	}

	return result.output
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

	proj := newProject(t, server.Config().ConnString())

	proj.schema(schemaV1)
	proj.must("plan", "init")
	proj.must("verify")
	proj.must("lint")
	proj.must("apply", "--url", url)
	proj.must("contract", "--url", url)

	if output := proj.must("status", "--url", url); strings.Contains(output, "pending") {
		t.Fatalf("status after apply:\n%s", output)
	}

	if output := proj.must("plan", "again"); !strings.Contains(output, "no changes") {
		t.Fatalf("second plan:\n%s", output)
	}

	_, err = target.Exec(ctx, `INSERT INTO app.users (email) VALUES ('a@x'), ('b@x');
		INSERT INTO app.orders (user_id, total) SELECT id, 10 FROM app.users`)
	if err != nil {
		t.Fatal(err)
	}

	proj.schema(schemaV2)

	output := proj.must("plan", "status")
	for _, want := range []string{"fixed RTL-L02", "fixed RTL-L04", "fixed RTL-C03"} {
		if !strings.Contains(output, want) {
			t.Errorf("plan output lacks %q:\n%s", want, output)
		}
	}

	proj.must("apply", "--url", url)
	proj.must("contract", "--url", url)
	proj.must("drift", "--url", url)

	var labels string
	if err := target.QueryRow(ctx, `SELECT string_agg(enumlabel, '|' ORDER BY enumsortorder) FROM pg_enum
		WHERE enumtypid = 'app.state'::regtype`).Scan(&labels); err != nil || labels != `new|finished|it's \ fine` {
		t.Fatalf("renamed labels = %q, %v", labels, err)
	}

	var filled bool
	if err := target.QueryRow(ctx, `SELECT bool_and(status = 'active') FROM app.users`).Scan(&filled); err != nil ||
		!filled {
		t.Fatalf("backfilled status: %v, %v", filled, err)
	}

	if _, err := target.Exec(ctx, `ALTER TABLE app.users ADD COLUMN extra int`); err != nil {
		t.Fatal(err)
	}

	drift := proj.ratel("drift", "--url", url)
	if drift.code != 1 || !strings.Contains(drift.output, "column app.users.extra") {
		t.Fatalf("drift = %+v", drift)
	}

	checkHandWritten(t, proj, target, url)
	checkTamper(t, proj)
}

// checkHandWritten writes a migration by hand: new creates it, verify and
// apply refuse it until hash seals it.
func checkHandWritten(t *testing.T, proj project, target *pgxpool.Pool, url string) {
	t.Helper()

	created := proj.must("new", "lower_emails")

	name, ok := strings.CutPrefix(strings.TrimSpace(created), "wrote ")
	if !ok {
		t.Fatalf("new = %q", created)
	}

	path := filepath.Join(proj.root, "migrations", name)

	template, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	write(t, path, string(template)+"-- ratel:step phase=backfill\nUPDATE app.users SET email = upper(email);\n")

	if verify := proj.ratel("verify"); verify.code != 1 || !strings.Contains(verify.output, "ratel.sum") {
		t.Fatalf("verify of an edited file = %+v", verify)
	}

	if sealed := proj.must("hash"); !strings.Contains(sealed, "sealed "+name) {
		t.Fatalf("hash = %q", sealed)
	}

	proj.must("verify")
	proj.must("apply", "--url", url)

	var upper bool

	err = target.QueryRow(t.Context(), `SELECT bool_and(email = upper(email)) FROM app.users`).Scan(&upper)
	if err != nil || !upper {
		t.Fatalf("hand-written backfill: %v, %v", upper, err)
	}
}

// checkTamper edits a planned file and adds a hand-written one breaking a
// rule: verify fails on the first, lint on the second.
func checkTamper(t *testing.T, proj project) {
	t.Helper()

	hand := filepath.Join(proj.root, "migrations", "99999999999999_hand.sql")
	write(t, hand, "CREATE INDEX orders_total ON app.orders (total);\n")

	if lint := proj.ratel("lint"); lint.code != 1 || !strings.Contains(lint.output, "error RTL-L02") {
		t.Fatalf("lint of a blocking index = %+v", lint)
	}

	if proj.ratel("verify").code != 1 {
		t.Fatal("verify accepted a file missing from ratel.sum")
	}
}
