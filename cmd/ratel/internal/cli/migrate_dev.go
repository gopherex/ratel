package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/intent"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/lint"
	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/probe"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// errDrift is a target that differs from the state its migrations produce.
var errDrift = errors.New("the target drifted from its migrations")

// userSchemas are the schemas of a database that migrations own.
const userSchemas = `SELECT nspname FROM pg_catalog.pg_namespace
WHERE nspname !~ '^pg_' AND nspname NOT IN ('information_schema', 'ratel') ORDER BY nspname`

// withDev runs body with a dev server, stopped afterwards.
func (w *workspace) withDev(ctx context.Context, body func(*devdb.Server) error) error {
	server, err := devdb.Start(ctx, w.settings.Dev)
	if err != nil {
		return err
	}

	defer func() { _ = server.Close(context.WithoutCancel(ctx)) }()

	return body(server)
}

// database is a fresh dev database filled by fill, dropped by the returned
// function.
func database(ctx context.Context, server *devdb.Server, fill func(*pgx.Conn) error) (*devdb.Database, error) {
	created, err := server.Database(ctx)
	if err != nil {
		return nil, err
	}

	connection, err := created.Pool.Acquire(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("dev database: %w", err), created.Drop(context.WithoutCancel(ctx)))
	}

	defer connection.Release()

	if err := fill(connection.Conn()); err != nil {
		return nil, errors.Join(err, created.Drop(context.WithoutCancel(ctx)))
	}

	return created, nil
}

// namespaces are the schemas both sides of a diff own: the desired
// catalog's and those the current state has.
func (w *workspace) namespaces(ctx context.Context, pools ...*pgxpool.Pool) ([]string, error) {
	var names []string

	for _, schema := range w.catalog.GetSchemas() {
		names = append(names, schema.GetName())
	}

	for _, pool := range pools {
		rows, err := pool.Query(ctx, userSchemas)
		if err != nil {
			return nil, fmt.Errorf("schemas: %w", err)
		}

		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("schemas: %w", err)
		}

		names = append(names, found...)
	}

	slices.Sort(names)

	return slices.Compact(names), nil
}

// states are the dev databases of the migrated and the desired state.
type states struct{ current, desired *devdb.Database }

func (s states) drop(ctx context.Context) {
	for _, database := range []*devdb.Database{s.current, s.desired} {
		if database != nil {
			_ = database.Drop(context.WithoutCancel(ctx))
		}
	}
}

func (w *workspace) states(ctx context.Context, server *devdb.Server, files []migrate.File) (states, error) {
	var result states

	current, err := database(ctx, server, func(connection *pgx.Conn) error {
		return migrate.ApplyDev(ctx, connection, files)
	})
	if err != nil {
		return result, err
	}

	result.current = current

	desired, err := database(ctx, server, func(connection *pgx.Conn) error {
		return migrate.ApplySchema(ctx, connection, w.parser, w.schema)
	})
	if err != nil {
		result.drop(ctx)

		return states{}, err
	}

	result.desired = desired

	return result, nil
}

// plan diffs the desired schema against the state the migrations produce
// and writes the planned steps as a new file with its ratel.sum.
func (w *workspace) plan(ctx context.Context, name string) error {
	if err := w.verify(); err != nil {
		return err
	}

	files, err := w.files()
	if err != nil {
		return err
	}

	return w.withDev(ctx, func(server *devdb.Server) error {
		both, err := w.states(ctx, server, files)
		if err != nil {
			return err
		}

		defer both.drop(ctx)

		planned, findings, err := w.planned(ctx, both)
		if err != nil {
			return err
		}

		w.report(findings)

		if failed := lint.Failed(findings); len(failed) > 0 {
			return fmt.Errorf("%w: %d", errFindings, len(failed))
		}

		if len(planned.Steps) == 0 {
			fmt.Fprintln(w.stdout, "no changes")

			return nil
		}

		return w.write(name, planned)
	})
}

// planned is the plan from the current to the desired state and its lint.
func (w *workspace) planned(ctx context.Context, both states) (plan.Plan, []lint.Finding, error) {
	names, err := w.namespaces(ctx, both.current.Pool)
	if err != nil {
		return plan.Plan{}, nil, err
	}

	current, err := introspect.Render(ctx, both.current.Pool, names)
	if err != nil {
		return plan.Plan{}, nil, err
	}

	desired, err := introspect.Render(ctx, both.desired.Pool, names)
	if err != nil {
		return plan.Plan{}, nil, err
	}

	intents := intent.From(w.catalog, w.quoter)
	changes := diff.Diff(current, desired, intents.Diff)

	env, err := probe.Env(ctx, both.current.Pool, current, desired, changes, names)
	if err != nil {
		return plan.Plan{}, nil, err
	}

	env.Safety.Major = w.settings.Postgres.Version
	planned := plan.Build(changes, &env, intents.Plan)

	findings := lint.Planner(planned.Findings, intents.Plan, planned.Steps)
	findings = append(findings, lint.Changes(changes, intents.Plan)...)
	findings = append(findings, lint.Schema(w.catalog)...)

	return planned, findings, nil
}

func (w *workspace) report(findings []lint.Finding) {
	lint.Sort(findings)

	for _, finding := range findings {
		fmt.Fprintln(w.stdout, finding)
	}
}

// write saves a plan as a new migration file and records it in ratel.sum.
func (w *workspace) write(name string, planned plan.Plan) error {
	file := migrate.FileName(time.Now(), name)
	contents := migrate.Write(file, planned, w.settings.Postgres.Version)

	if err := atomicWrite(filepath.Join(w.directory, file), contents, fileMode); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}

	sources := append(slices.Clone(w.sources), migrate.Source{Name: file, SQL: string(contents)})

	if err := atomicWrite(filepath.Join(w.directory, migrate.SumFile), migrate.Sum(sources), fileMode); err != nil {
		return fmt.Errorf("write %s: %w", migrate.SumFile, err)
	}

	fmt.Fprintf(w.stdout, "wrote %s: %d steps\n", file, len(planned.Steps))

	return nil
}

// lint applies the migration files one by one on a dev database, linting
// each selected file, or every file, against the state before it, then
// lints the desired schema.
func (w *workspace) lint(ctx context.Context, selected []string) error {
	files, err := w.files()
	if err != nil {
		return err
	}

	var findings []lint.Finding

	err = w.withDev(ctx, func(server *devdb.Server) error {
		target, err := database(ctx, server, func(*pgx.Conn) error { return nil })
		if err != nil {
			return err
		}

		defer func() { _ = target.Drop(context.WithoutCancel(ctx)) }()

		for index := range files {
			file := &files[index]

			if len(selected) == 0 || slices.Contains(selected, file.Name) {
				found, err := w.lintFile(ctx, target.Pool, file)
				if err != nil {
					return err
				}

				findings = append(findings, found...)
			}

			if err := applyOn(ctx, target.Pool, *file); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	findings = append(findings, lint.Schema(w.catalog)...)
	w.report(findings)

	if failed := lint.Failed(findings); len(failed) > 0 {
		return fmt.Errorf("%w: %d", errFindings, len(failed))
	}

	return nil
}

// lintFile lints a file against the state of target before it.
func (w *workspace) lintFile(ctx context.Context, target *pgxpool.Pool, file *migrate.File) ([]lint.Finding, error) {
	names, err := w.namespaces(ctx, target)
	if err != nil {
		return nil, err
	}

	current, err := introspect.Render(ctx, target, names)
	if err != nil {
		return nil, err
	}

	env, err := probe.Env(ctx, target, current, current, nil, names)
	if err != nil {
		return nil, err
	}

	env.Safety.Major = w.settings.Postgres.Version
	migrate.Classify(file, safety.NewClassifier(w.parser, env.Safety), w.parser)

	policies := make(map[string]bool)

	for _, object := range current.Objects {
		if diff.Created(object).Kind == diff.CreatePolicy {
			policies[diff.Created(object).Table] = true
		}
	}

	return lint.Steps(file, env.Safety, policies), nil
}

func applyOn(ctx context.Context, pool *pgxpool.Pool, file migrate.File) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("dev database: %w", err)
	}

	defer connection.Release()

	return migrate.ApplyDev(ctx, connection.Conn(), []migrate.File{file})
}

// drift diffs the target against the state the migrations produce.
func (w *workspace) drift(ctx context.Context, url string) error {
	files, err := w.files()
	if err != nil {
		return err
	}

	target, err := migrate.Connect(ctx, url)
	if err != nil {
		return err
	}

	defer target.Close()

	return w.withDev(ctx, func(server *devdb.Server) error {
		expected, err := database(ctx, server, func(connection *pgx.Conn) error {
			return migrate.ApplyDev(ctx, connection, files)
		})
		if err != nil {
			return err
		}

		defer func() { _ = expected.Drop(context.WithoutCancel(ctx)) }()

		names, err := w.namespaces(ctx, expected.Pool, target)
		if err != nil {
			return err
		}

		want, err := introspect.Render(ctx, expected.Pool, names)
		if err != nil {
			return err
		}

		got, err := introspect.Render(ctx, target, names)
		if err != nil {
			return err
		}

		changes := diff.Diff(want, got, diff.Intents{})
		for _, change := range changes {
			fmt.Fprintf(w.stdout, "%s %s\n", change.Kind, change.Key)
		}

		if len(changes) > 0 {
			return fmt.Errorf("%w: %d differences", errDrift, len(changes))
		}

		return nil
	})
}
