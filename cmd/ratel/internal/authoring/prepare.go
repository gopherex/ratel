// Package authoring writes, classifies and seals migration files and applies
// them to dev databases: the parts of the migration engine that need the
// parser. The runner that applies them to a target is package
// github.com/gopherex/ratel/migrate.
package authoring

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Classify fills in what a hand-written step does from its statements: its
// operations, whether it can run in a transaction, its class and the locks
// it takes, every relation taken to exist. A statement the parser cannot
// read is an unknown operation, assessed as the worst case. Planned steps
// keep their header.
func Classify(file *File, classifier *safety.Classifier, parser parse.Parser) {
	for index := range file.Steps {
		step := &file.Steps[index]
		if step.Planned() {
			continue
		}

		step.Operations, step.PerStatement = nil, nil

		for _, text := range step.Statements {
			operations := []safety.Operation{{Kind: safety.Unknown}}
			if statements, err := parser.Statements(text); err == nil && len(statements) == 1 {
				operations = classifier.Operations(statements[0].Node)
			}

			// A data change in a backfill step is a batch of that backfill.
			for operation := range operations {
				if step.Phase == migrate.PhaseBackfill && operations[operation].Kind == safety.DataChange {
					operations[operation].Kind = safety.Backfill
				}
			}

			step.PerStatement = append(step.PerStatement, operations)
			step.Operations = append(step.Operations, operations...)
		}

		env := safety.Env{Relations: make(map[string]safety.Relation)}
		for _, operation := range step.Operations {
			env.Relations[operation.Relation] = safety.Relation{}
			env.Relations[operation.Other] = safety.Relation{}
		}

		assessed := safety.Assess(step.Operations, env)
		step.Tx, step.Locks = assessed.Tx, assessed.Locks

		switch {
		case step.Class != "":
			// Declared in the step header.
		case step.Phase == migrate.PhaseBackfill:
			step.Class = migrate.ClassBatched
		case step.Tx == migrate.OwnStatement:
			step.Class = migrate.ClassLong
		default:
			step.Class = migrate.ClassShort
		}
	}
}

// ApplyDev runs every step of the files in order on a dev database, without
// guards, batching or journal: the dev database holds no data and no other
// sessions, and the result is only rendered.
func ApplyDev(ctx context.Context, database *pgx.Conn, files []File) error {
	for _, file := range files {
		for index := range file.Steps {
			if err := applyDevStep(ctx, database, &file.Steps[index]); err != nil {
				return fmt.Errorf("%s step %s: %w", file.Name, file.Steps[index].ID, err)
			}
		}
	}

	return nil
}

func applyDevStep(ctx context.Context, database *pgx.Conn, step *Step) error {
	if step.Tx == migrate.OwnStatement {
		for _, statement := range step.Statements {
			if _, err := database.Exec(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}

		return nil
	}

	transaction, err := database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	for _, statement := range append([]string{"SET LOCAL check_function_bodies = off"}, step.Statements...) {
		if _, err := transaction.Exec(ctx, statement); err != nil {
			return errors.Join(fmt.Errorf("%s: %w", statement, err), transaction.Rollback(ctx))
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}
