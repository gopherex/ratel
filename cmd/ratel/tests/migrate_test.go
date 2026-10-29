//go:build integration

package tests_test

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/pkg/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// TestApplySchema applies schema statements in reverse order, with a
// foreign key cycle, and checks the result renders as the ordered DDL does.
func TestApplySchema(t *testing.T) {
	t.Parallel()

	shop, err := os.ReadFile("testdata/introspect/schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	pool := postgresPool(t, 18)

	setup, cancelSetup := context.WithTimeout(t.Context(), startupTimeout)
	t.Cleanup(cancelSetup)

	if _, err := pool.Exec(setup, `CREATE ROLE ratel_reader`); err != nil {
		t.Fatal(err)
	}

	server, err := devdb.Start(setup, config.Dev{URL: pool.Config().ConnString()}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = server.Close(context.WithoutCancel(setup)) })

	cycle := `CREATE TABLE cyc.a (id int PRIMARY KEY, b int REFERENCES cyc.b);
CREATE TABLE cyc.b (id int PRIMARY KEY, a int, CONSTRAINT b_a FOREIGN KEY (a) REFERENCES cyc.a);
CREATE SCHEMA cyc;`

	for _, tc := range []struct {
		name, ddl, ordered string
		namespace          string
	}{
		{name: "shop reversed", ddl: string(shop), ordered: string(shop), namespace: "shop"},
		{
			name: "foreign key cycle", ddl: cycle, namespace: "cyc",
			ordered: `CREATE SCHEMA cyc; CREATE TABLE cyc.a (id int PRIMARY KEY, b int);
CREATE TABLE cyc.b (id int PRIMARY KEY, a int, CONSTRAINT b_a FOREIGN KEY (a) REFERENCES cyc.a);
ALTER TABLE cyc.a ADD FOREIGN KEY (b) REFERENCES cyc.b;`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parser := newParser(t)

			t.Parallel()

			// The deadline starts here: a parallel subtest waits for a free slot
			// after its parent returns, and that wait must not eat its budget.
			ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
			defer cancel()

			files := reversed(t, parser, tc.ddl)

			got := database(ctx, t, server, func(connection *pgx.Conn) error {
				return authoring.ApplySchema(ctx, connection, parser, files)
			}, tc.namespace)
			want := database(ctx, t, server, func(connection *pgx.Conn) error {
				_, err := connection.Exec(ctx, tc.ordered)

				return err
			}, tc.namespace)

			if difference := cmp.Diff(want.Objects, got.Objects); difference != "" {
				t.Fatalf("reversed schema (-ordered +applied):\n%s", difference)
			}
		})
	}
}

func newParser(t *testing.T) parse.Parser {
	t.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	return parser
}

// reversed is one file per statement, in reverse order.
func reversed(t *testing.T, parser parse.Parser, ddl string) []catalog.File {
	t.Helper()

	result, err := parser.Parse(ddl)
	if err != nil {
		t.Fatal(err)
	}

	files := make([]catalog.File, 0, len(result.GetStmts()))

	for _, raw := range result.GetStmts() {
		start := int(raw.GetStmtLocation())
		end := len(ddl)

		if raw.GetStmtLen() > 0 {
			end = start + int(raw.GetStmtLen())
		}

		files = append(files, catalog.File{Name: "f.sql", SQL: ddl[start:end]})
	}

	slices.Reverse(files)

	return files
}

// database renders a fresh dev database after fill ran on it.
func database(
	ctx context.Context, t *testing.T, server *devdb.Server, fill func(*pgx.Conn) error, namespace string,
) introspect.Rendering {
	t.Helper()

	created, err := server.Database(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = created.Drop(context.WithoutCancel(ctx)) })

	connection, err := created.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer connection.Release()

	if err := fill(connection.Conn()); err != nil {
		t.Fatal(err)
	}

	return render(ctx, t, created, []string{namespace})
}

// TestApplyDev writes a plan, parses it back with a hand-written file, and
// applies both to a dev database.
func TestApplyDev(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	pool := postgresPool(t, 18)

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	hand, err := authoring.Parse(migrate.Source{Name: "1_hand.sql", SQL: `CREATE SCHEMA app;
CREATE TABLE app.t (id int PRIMARY KEY, v int);
CREATE INDEX CONCURRENTLY t_v ON app.t (v);
-- ratel:step phase=backfill
UPDATE app.t SET v = 1;`})
	if err != nil {
		t.Fatal(err)
	}

	authoring.Classify(&hand, safety.NewClassifier(parser, safety.Env{}), parser)

	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer connection.Release()

	if err := authoring.ApplyDev(ctx, connection.Conn(), []authoring.File{hand}); err != nil {
		t.Fatal(err)
	}

	var valid bool
	if err := pool.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = 'app.t_v'::regclass`).
		Scan(&valid); err != nil || !valid {
		t.Fatalf("concurrent index: %v, %v", valid, err)
	}
}
