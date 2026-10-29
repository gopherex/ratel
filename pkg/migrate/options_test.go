package migrate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gopherex/ratel/pkg/migrate"
)

func TestSettings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		change func(*migrate.Settings)
		valid  bool
	}{
		{"defaults", func(*migrate.Settings) {}, true},
		{"no pause", func(s *migrate.Settings) { s.Backfill.Pause = 0 }, true},
		{"zero lock timeout", func(s *migrate.Settings) { s.LockTimeout = 0 }, false},
		{"zero total wait", func(s *migrate.Settings) { s.MaxTotalWait = 0 }, false},
		{"zero statement timeout", func(s *migrate.Settings) { s.StatementTimeout = 0 }, false},
		{"zero long transaction", func(s *migrate.Settings) { s.LongTxThreshold = 0 }, false},
		{"zero batch", func(s *migrate.Settings) { s.Backfill.Batch = 0 }, false},
		{"negative pause", func(s *migrate.Settings) { s.Backfill.Pause = -time.Second }, false},
		{"zero replica lag", func(s *migrate.Settings) { s.Backfill.MaxReplicaLag = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			settings := migrate.DefaultSettings()
			tc.change(&settings)

			err := migrate.Validate(settings)
			if (err == nil) != tc.valid || err != nil && !errors.Is(err, migrate.ErrSettings) {
				t.Errorf("validate() = %v, valid %v", err, tc.valid)
			}
		})
	}
}

func TestDefaultSettings(t *testing.T) {
	t.Parallel()

	settings := migrate.DefaultSettings()
	if settings.LockTimeout != 100*time.Millisecond || settings.MaxTotalWait != 10*time.Minute ||
		settings.StatementTimeout != 5*time.Second || settings.LongTxThreshold != time.Minute ||
		settings.Backfill.Batch != 1000 {
		t.Errorf("DefaultSettings() = %+v", settings)
	}
}

func BenchmarkValidate(b *testing.B) {
	settings := migrate.DefaultSettings()

	for b.Loop() {
		if err := migrate.Validate(settings); err != nil {
			b.Fatal(err)
		}
	}
}
