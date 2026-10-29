package migrate_test

import (
	"testing"
	"time"

	"github.com/gopherex/ratel/migrate"
)

func TestConflicting(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode migrate.LockMode
		want string
	}{
		{migrate.AccessShare, "AccessExclusiveLock"},
		{
			migrate.ShareUpdateExclusive,
			"ShareUpdateExclusiveLock,ShareLock,ShareRowExclusiveLock,ExclusiveLock,AccessExclusiveLock",
		},
	} {
		if got := migrate.Conflicting(tc.mode); got != tc.want {
			t.Errorf("conflicting(%s) = %q", tc.mode, got)
		}
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
		migrate.Conflicting(migrate.ShareRowExclusive)
	}
}
