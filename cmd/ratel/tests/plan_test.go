//go:build integration

package tests_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/probe"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// planCase plans a diff case: data is inserted into the current database
// first, and check must hold after every phase ran.
type planCase struct {
	diffCase
	intents plan.Intents
	data    string
	check   string
}

func planCases(t *testing.T) []planCase {
	t.Helper()

	items := diffCases(t)
	cases := make([]planCase, 0, len(items))

	for index := range items {
		item := &items[index]
		cases = append(cases, planCase{diffCase: *item, intents: plan.Intents{
			Allow: map[string][]string{
				"column app.t.a": {"RTL-R02"}, "column app.t.g": {"RTL-R01"}, "table app.solo": {"RTL-R03"},
				"table app.loose": {"RTL-L07"}, "type app.d": {"RTL-L06"}, "column app.t.n": {"RTL-R02"},
			},
			Retype: item.intents.Retype,
		}})
	}

	return append(cases, recipeCases()...)
}

// recipeCases exercise the safe recipes on tables holding rows.
func recipeCases() []planCase {
	table := `CREATE TABLE app.t (id integer PRIMARY KEY, v integer, s text);`
	rows := `INSERT INTO app.t SELECT g AS id, g AS v, 'r' || g FROM generate_series(1, 25) g;`

	return []planCase{
		{
			diffCase: diffCase{
				name: "constraints and indexes", current: table + `CREATE INDEX t_s ON app.t (s);`,
				desired: `CREATE TABLE app.t (id integer PRIMARY KEY, v integer NOT NULL CHECK (v > 0), s text UNIQUE);
					CREATE TABLE app.r (tid integer REFERENCES app.t);
					CREATE INDEX t_s ON app.t (lower(s)); CREATE INDEX t_v ON app.t (v);`,
			},
			data: rows, check: `SELECT count(*) = 25 FROM app.t`,
		},
		{
			diffCase: diffCase{
				name: "primary key", current: `CREATE TABLE app.t (id integer, v integer);`,
				desired: `CREATE TABLE app.t (id integer PRIMARY KEY, v integer);`,
			},
			data: `INSERT INTO app.t SELECT g, g FROM generate_series(1, 25) g;`, check: `SELECT count(*) = 25 FROM app.t`,
		},
		{
			diffCase: diffCase{
				name: "backfilled column", current: table,
				desired: `CREATE TABLE app.t (id integer PRIMARY KEY, v integer, s text, f text NOT NULL,
					u float8 DEFAULT random());`,
			},
			intents: plan.Intents{Backfill: map[string]string{
				"column app.t.f": "'filled'", "column app.t.u": "0.5",
			}},
			data:  rows,
			check: `SELECT bool_and(f = 'filled' AND u = 0.5) FROM app.t`,
		},
		{
			diffCase: diffCase{
				name: "renamed column", current: table + `CREATE INDEX t_v ON app.t (v);`,
				desired: `CREATE TABLE app.t (id integer PRIMARY KEY, w integer, s text); CREATE INDEX t_v ON app.t (w);`,
				intents: diff.Intents{Renamed: map[string]string{"column app.t.w": "v"}},
			},
			data: rows, check: `SELECT sum(w) = 325 FROM app.t`,
		},
		{
			diffCase: diffCase{
				name: "retyped column",
				current: table + `CREATE INDEX t_v ON app.t (v); ALTER TABLE app.t ADD CONSTRAINT t_v_check CHECK (v > 0);
					CREATE VIEW app.tv AS SELECT id, v FROM app.t;`,
				desired: `CREATE TABLE app.t (id integer PRIMARY KEY, v bigint NOT NULL, s text);
					CREATE INDEX t_v ON app.t (v); ALTER TABLE app.t ADD CONSTRAINT t_v_check CHECK (v > 0);
					CREATE VIEW app.tv AS SELECT id, v FROM app.t;`,
			},
			intents: plan.Intents{Retype: map[string]string{"column app.t.v": "v::bigint"}},
			data:    rows, check: `SELECT sum(v) = 325 FROM app.tv`,
		},
		{
			diffCase: diffCase{
				name: "replaced with users",
				current: table + `CREATE VIEW app.a AS SELECT id, v FROM app.t; CREATE VIEW app.b AS SELECT id FROM app.a;
					CREATE FUNCTION app.f(x integer) RETURNS integer LANGUAGE sql AS 'SELECT x';
					CREATE VIEW app.c AS SELECT app.f(id) AS f FROM app.t;`,
				desired: table + `CREATE VIEW app.a AS SELECT id, v::text AS v FROM app.t;
					CREATE VIEW app.b AS SELECT id FROM app.a;
					CREATE FUNCTION app.f(x integer) RETURNS bigint LANGUAGE sql AS 'SELECT x::bigint';
					CREATE VIEW app.c AS SELECT app.f(id) AS f FROM app.t;`,
			},
			data: rows, check: `SELECT count(*) = 25 FROM app.b`,
		},
		{
			diffCase: diffCase{
				name: "partitioned index",
				current: `CREATE TABLE app.p (k integer NOT NULL, v integer) PARTITION BY RANGE (k);
					CREATE TABLE app.p1 PARTITION OF app.p FOR VALUES FROM (0) TO (10);
					CREATE TABLE app.p2 PARTITION OF app.p FOR VALUES FROM (10) TO (20) PARTITION BY RANGE (k);
					CREATE TABLE app.p21 PARTITION OF app.p2 FOR VALUES FROM (10) TO (15);
					CREATE TABLE app.loose (k integer NOT NULL, v integer);`,
				desired: `CREATE TABLE app.p (k integer NOT NULL, v integer) PARTITION BY RANGE (k);
					CREATE TABLE app.p1 PARTITION OF app.p FOR VALUES FROM (0) TO (10);
					CREATE TABLE app.p2 PARTITION OF app.p FOR VALUES FROM (10) TO (20) PARTITION BY RANGE (k);
					CREATE TABLE app.p21 PARTITION OF app.p2 FOR VALUES FROM (10) TO (15);
					CREATE TABLE app.loose PARTITION OF app.p FOR VALUES FROM (20) TO (30);
					CREATE INDEX p_v ON app.p (v);`,
			},
			data:  `INSERT INTO app.p SELECT g, g FROM generate_series(0, 14) g; INSERT INTO app.loose VALUES (25, 1);`,
			check: `SELECT count(*) = 16 FROM app.p`,
		},
	}
}

// TestPlan applies every planned step phase by phase to the current
// database and checks it then renders as the desired one with its rows.
func TestPlan(t *testing.T) {
	t.Parallel()

	cases := planCases(t)

	for _, major := range []int{16, 17, 18} {
		t.Run(strconv.Itoa(major), func(t *testing.T) {
			t.Parallel()

			pool := postgresPool(t, major)

			ctx, cancel := context.WithTimeout(t.Context(), 3*startupTimeout)
			defer cancel()

			if _, err := pool.Exec(ctx, `CREATE ROLE ratel_reader`); err != nil {
				t.Fatal(err)
			}

			server, err := devdb.Start(ctx, config.Dev{URL: pool.Config().ConnString()})
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = server.Close(context.WithoutCancel(ctx)) }()

			for _, item := range cases {
				t.Run(item.name, func(t *testing.T) { checkPlan(ctx, t, server, item) })
			}
		})
	}
}

func checkPlan(ctx context.Context, t *testing.T, server *devdb.Server, item planCase) {
	t.Helper()

	namespaces := []string{"app"}
	if item.namespace != "" {
		namespaces = []string{item.namespace}
	}

	current := applied(ctx, t, server, namespaces, item.current+item.data)
	desired := applied(ctx, t, server, namespaces, item.desired)
	want := render(ctx, t, desired, namespaces)
	have := render(ctx, t, current, namespaces)
	changes := diff.Diff(have, want, item.diffCase.intents)

	env, err := probe.Env(ctx, current.Pool, have, want, changes, namespaces)
	if err != nil {
		t.Fatal(err)
	}

	planned := plan.Build(changes, &env, item.intents)

	for _, finding := range planned.Findings {
		if !finding.Fixed {
			t.Logf("finding %s on %s: %s", finding.Code, finding.Change, finding.Detail)
		}
	}

	for index := range planned.Steps {
		runStep(ctx, t, current, &planned.Steps[index])
	}

	got := render(ctx, t, current, namespaces)
	want, got = byName(want), byName(got)

	for _, part := range []struct {
		name      string
		want, got any
	}{
		{"objects", want.Objects, got.Objects},
		{"columns", want.Columns, got.Columns},
		{"domains", want.Domains, got.Domains},
	} {
		if difference := cmp.Diff(part.want, part.got); difference != "" {
			t.Fatalf("%s after the plan (-desired +migrated):\n%s\nplan:\n%s", part.name, difference, describe(planned))
		}
	}

	if item.check != "" {
		var held bool
		if err := current.Pool.QueryRow(ctx, item.check).Scan(&held); err != nil || !held {
			t.Fatalf("check %q: %v, %v", item.check, held, err)
		}
	}
}

// runStep applies a step the plain way: a short step in one transaction, a
// long one statement by statement, a backfill in one UPDATE.
func runStep(ctx context.Context, t *testing.T, database *devdb.Database, step *plan.Step) {
	t.Helper()

	if step.Tx == safety.OwnStatement {
		for _, statement := range step.Statements {
			if _, err := database.Pool.Exec(ctx, statement.SQL); err != nil {
				t.Fatalf("%s step %s: %v\n%s", step.Phase, step.Change, err, statement.SQL)
			}
		}

		return
	}

	transaction, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = transaction.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := transaction.Exec(ctx, `SET LOCAL check_function_bodies = off`); err != nil {
		t.Fatal(err)
	}

	for _, statement := range step.Statements {
		if _, err := transaction.Exec(ctx, statement.SQL); err != nil {
			t.Fatalf("%s step %s: %v\n%s", step.Phase, step.Change, err, statement.SQL)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// byName orders table columns by name: a migration adds a column last,
// whatever its place in the schema, so only the set of columns is compared.
func byName(rendering introspect.Rendering) introspect.Rendering {
	result := rendering
	result.Objects = slices.Clone(rendering.Objects)
	result.Columns = slices.Clone(rendering.Columns)

	slices.SortFunc(result.Columns, func(a, b introspect.Column) int { return strings.Compare(a.Key, b.Key) })

	for index := range result.Objects {
		object := &result.Objects[index]

		head, rest, ok := strings.Cut(object.SQL, " (")
		if !strings.HasPrefix(object.Key, "table ") || !ok || strings.Contains(head, " PARTITION OF ") {
			continue
		}

		closing := closingParen(rest)
		columns := splitTopLevel(rest[:closing])
		slices.Sort(columns)
		object.SQL = head + " (" + strings.Join(columns, ", ") + rest[closing:]
	}

	return result
}

// closingParen is the index of the parenthesis closing an opened list.
func closingParen(text string) int {
	depth := 0

	for index, char := range text {
		switch char {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return index
			}

			depth--
		}
	}

	return len(text)
}

// splitTopLevel splits a list at the commas outside parentheses.
func splitTopLevel(text string) []string {
	var (
		parts []string
		depth int
		start int
	)

	for index, char := range text {
		switch {
		case char == '(':
			depth++
		case char == ')':
			depth--
		case char == ',' && depth == 0:
			parts = append(parts, strings.TrimSpace(text[start:index]))
			start = index + 1
		}
	}

	return append(parts, strings.TrimSpace(text[start:]))
}

func describe(planned plan.Plan) string {
	var out strings.Builder

	for index := range planned.Steps {
		step := &planned.Steps[index]
		out.WriteString("-- " + string(step.Phase) + " " + string(step.Class) + " " + step.Change + "\n")

		for _, statement := range step.Statements {
			out.WriteString(statement.SQL + "\n")
		}
	}

	return out.String()
}
