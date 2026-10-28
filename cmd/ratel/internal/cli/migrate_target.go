package cli

import (
	"context"
	"fmt"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

// status prints each step's state on the target.
func (w *workspace) status(ctx context.Context, url string) error {
	files, err := w.files()
	if err != nil {
		return err
	}

	target, err := migrate.Connect(ctx, url)
	if err != nil {
		return err
	}

	defer target.Close()

	steps, err := migrate.NewRunner(target, w.settings.Migrate, w.stdout).Status(ctx, files)
	if err != nil {
		return err
	}

	for _, step := range steps {
		fmt.Fprintf(w.stdout, "%s %s %s %s\n", step.State, step.Phase, step.File, step.ID)
	}

	return nil
}

// apply runs the pending steps of the phases on the target, after checking
// the files against ratel.sum.
func (w *workspace) apply(ctx context.Context, url string, phases ...plan.Phase) error {
	if err := w.verify(); err != nil {
		return err
	}

	files, err := w.files()
	if err != nil {
		return err
	}

	target, err := migrate.Connect(ctx, url)
	if err != nil {
		return err
	}

	defer target.Close()

	return migrate.NewRunner(target, w.settings.Migrate, w.stdout).Apply(ctx, files, phases...)
}
