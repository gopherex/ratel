package migrate

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
)

// State is where a step stands on the target.
type State string

// Step states.
const (
	Pending     State = "pending"
	Applied     State = "applied"
	Interrupted State = "interrupted" // started or failed, not done
	Changed     State = "changed"     // done, but its SQL differs now
)

// StepStatus is one step's state.
type StepStatus struct {
	File  string
	ID    string
	Phase Phase
	State State
}

// Status lists every step of the migrations with its state on the database.
// A database without a journal has every step pending.
func Status(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) ([]StepStatus, error) {
	files, err := load(migrations)
	if err != nil {
		return nil, err
	}

	var exists bool

	err = pool.QueryRow(ctx, `SELECT to_regclass('ratel.migrations_journal') IS NOT NULL`).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}

	recorded := make(map[string]entry)

	if exists {
		if recorded, err = journal(ctx, pool); err != nil {
			return nil, err
		}
	}

	var result []StepStatus

	for _, file := range files {
		for index := range file.Steps {
			step := &file.Steps[index]
			row, found := recorded[step.ID]
			result = append(result, StepStatus{
				File: file.Name, ID: step.ID, Phase: step.Phase, State: stateOf(row, found, Checksum(step.Statements)),
			})
		}
	}

	return result, nil
}

// stateOf is the state of a step from its journal row.
func stateOf(row entry, found bool, checksum string) State {
	switch {
	case found && row.outcome == outcomeDone && row.checksum != checksum:
		return Changed
	case found && row.outcome == outcomeDone:
		return Applied
	case found:
		return Interrupted
	default:
		return Pending
	}
}
