package migrate

import (
	"log/slog"
	"time"
)

// Settings tune how a run waits for locks and fills rows. DefaultSettings
// documents every default; ratel.yaml's migrate section sets the same values
// for the ratel CLI.
type Settings struct {
	// LockTimeout is the first lock_timeout of a short step; the GitLab retry
	// schedule scales from it.
	LockTimeout time.Duration
	// MaxTotalWait bounds how long a step waits for conflicting sessions and
	// retries its locks before it fails with ErrBlocked.
	MaxTotalWait time.Duration
	// StatementTimeout is the statement_timeout of every statement of a short
	// step and of every backfill batch.
	StatementTimeout time.Duration
	// LongTxThreshold makes a transaction older than it a blocker of every
	// step.
	LongTxThreshold time.Duration
	Backfill        BackfillSettings
}

// BackfillSettings tune batched backfills.
type BackfillSettings struct {
	Batch         int           // rows per batch
	Pause         time.Duration // between batches
	MaxReplicaLag time.Duration // a batch waits while a replica lags more
}

// DefaultSettings are the settings of a run without WithSettings.
func DefaultSettings() Settings {
	const (
		lockTimeout      = 100 * time.Millisecond
		maxTotalWait     = 10 * time.Minute
		statementTimeout = 5 * time.Second
		batch            = 1000
		pause            = 50 * time.Millisecond
		maxReplicaLag    = 10 * time.Second
	)

	return Settings{
		LockTimeout: lockTimeout, MaxTotalWait: maxTotalWait, StatementTimeout: statementTimeout,
		LongTxThreshold: time.Minute,
		Backfill:        BackfillSettings{Batch: batch, Pause: pause, MaxReplicaLag: maxReplicaLag},
	}
}

// Option configures a run.
type Option func(*options)

type options struct {
	settings Settings
	logger   *slog.Logger
}

// WithSettings replaces DefaultSettings.
func WithSettings(settings Settings) Option {
	return func(o *options) { o.settings = settings }
}

// WithLogger receives a record of every step a run starts; by default the
// records are discarded.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) { o.logger = logger }
}

func configure(list []Option) options {
	result := options{
		settings: DefaultSettings(),
		logger:   slog.New(slog.DiscardHandler),
	}

	for _, option := range list {
		option(&result)
	}

	return result
}
