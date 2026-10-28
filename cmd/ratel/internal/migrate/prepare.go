package migrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Classify fills in what a hand-written step does from its statements: its
// operations, whether it can run in a transaction and its class. A
// statement the parser cannot read is an unknown operation, assessed as the
// worst case. Planned steps keep their header.
func Classify(file *File, classifier *safety.Classifier, parser parse.Parser) {
	for index := range file.Steps {
		step := &file.Steps[index]
		if step.Planned {
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
				if step.Phase == plan.Backfill && operations[operation].Kind == safety.DataChange {
					operations[operation].Kind = safety.Backfill
				}
			}

			step.PerStatement = append(step.PerStatement, operations)
			step.Operations = append(step.Operations, operations...)
		}

		step.Tx = safety.Assess(step.Operations, safety.Env{}).Tx

		switch {
		case step.Class != "":
			// Declared in the step header.
		case step.Phase == plan.Backfill:
			step.Class = plan.Batched
		case step.Tx == safety.OwnStatement:
			step.Class = plan.Long
		default:
			step.Class = plan.Short
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
	if step.Tx == safety.OwnStatement {
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
