package migrate

import (
	"context"
	"fmt"

	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
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
	Phase plan.Phase
	State State
}

// Status lists every step of the files with its state on the target. A
// target without a journal has every step pending.
func (r *Runner) Status(ctx context.Context, files []File) ([]StepStatus, error) {
	var exists bool

	err := r.pool.QueryRow(ctx, `SELECT to_regclass('ratel.migrations_journal') IS NOT NULL`).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}

	recorded := make(map[string]entry)

	if exists {
		if recorded, err = r.journal(ctx); err != nil {
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
