package migrate_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
)

func TestStateOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name              string
		outcome, recorded string
		found             bool
		want              migrate.State
	}{
		{name: "pending", want: migrate.Pending},
		{name: "applied", outcome: "done", recorded: "x", found: true, want: migrate.Applied},
		{name: "changed", outcome: "done", recorded: "y", found: true, want: migrate.Changed},
		{name: "failed", outcome: "failed", recorded: "x", found: true, want: migrate.Interrupted},
		{name: "started", recorded: "x", found: true, want: migrate.Interrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := migrate.StateOf(tc.outcome, tc.recorded, tc.found, "x"); got != tc.want {
				t.Errorf("stateOf() = %s", got)
			}
		})
	}
}
