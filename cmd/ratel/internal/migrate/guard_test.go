package migrate_test

import (
	"testing"
	"time"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

func TestConflicting(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode safety.LockMode
		want string
	}{
		{safety.AccessShare, "AccessExclusiveLock"},
		{
			safety.ShareUpdateExclusive,
			"ShareUpdateExclusiveLock,ShareLock,ShareRowExclusiveLock,ExclusiveLock,AccessExclusiveLock",
		},
	} {
		if got := migrate.Conflicting(tc.mode); got != tc.want {
			t.Errorf("conflicting(%s) = %q", tc.mode, got)
		}
	}
}

func TestLocks(t *testing.T) {
	t.Parallel()

	hand := &migrate.Step{Operations: []safety.Operation{{Kind: safety.AddColumn, Relation: "app.t"}}}
	if got := migrate.Locks(hand); len(got) != 1 || got[0].Mode != safety.AccessExclusive {
		t.Errorf("hand-written locks = %v", got)
	}

	planned := &migrate.Step{Planned: true, Operations: hand.Operations}
	if got := migrate.Locks(planned); len(got) != 0 {
		t.Errorf("planned locks come from the header: %v", got)
	}
}

func TestMilliseconds(t *testing.T) {
	t.Parallel()

	if got := migrate.Milliseconds(1500 * time.Millisecond); got != "'1500ms'" {
		t.Errorf("milliseconds() = %q", got)
	}
}

func BenchmarkConflicting(b *testing.B) {
	for b.Loop() {
		migrate.Conflicting(safety.ShareRowExclusive)
	}
}
