package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel/migrate"
)

// connect opens a pool on the target; connections open on first use.
func connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}

	return pool, nil
}

// status prints each step's state on the target.
func (w *workspace) status(ctx context.Context, url string) error {
	target, err := connect(ctx, url)
	if err != nil {
		return err
	}

	defer target.Close()

	steps, err := migrate.Status(ctx, target, os.DirFS(w.directory))
	if err != nil {
		return err
	}

	for _, step := range steps {
		fmt.Fprintf(w.stdout, "%s %s %s %s\n", step.State, step.Phase, step.File, step.ID)
	}

	return nil
}

// runFunc is migrate.Apply or migrate.Contract.
type runFunc func(context.Context, *pgxpool.Pool, fs.FS, ...migrate.Option) error

// apply runs the pending steps on the target with the settings of
// ratel.yaml, reporting each step it starts.
func (w *workspace) apply(ctx context.Context, url string, run runFunc) error {
	target, err := connect(ctx, url)
	if err != nil {
		return err
	}

	defer target.Close()

	configured := w.settings.Migrate
	settings := migrate.Settings{
		LockTimeout: configured.LockTimeout, MaxTotalWait: configured.MaxTotalWait,
		StatementTimeout: configured.StatementTimeout, LongTxThreshold: configured.LongTxThreshold,
		Backfill: migrate.BackfillSettings{
			Batch: configured.Backfill.Batch, Pause: configured.Backfill.Pause,
			MaxReplicaLag: configured.Backfill.MaxReplicaLag,
		},
	}

	return run(ctx, target, os.DirFS(w.directory), migrate.WithSettings(settings), migrate.WithLogger(w.log))
}
