package migrate_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
)

func TestLockKey(t *testing.T) {
	t.Parallel()

	key := migrate.LockKey("app")
	if len(key) != 64 || strings.Trim(key, "01") != "" || key == migrate.LockKey("other") {
		t.Errorf("lockKey() = %q", key)
	}
}

func TestSpread(t *testing.T) {
	t.Parallel()

	for range 100 {
		if got := migrate.Spread(time.Second); got < 750*time.Millisecond || got >= 1250*time.Millisecond {
			t.Fatalf("spread() = %v", got)
		}
	}

	if migrate.Spread(0) != 0 {
		t.Error("spread(0) is not 0")
	}
}

func TestChecksum(t *testing.T) {
	t.Parallel()

	if migrate.Checksum([]string{"a", "b"}) == migrate.Checksum([]string{"a;\nb"}) &&
		migrate.Checksum([]string{"a"}) == migrate.Checksum([]string{"b"}) {
		t.Error("checksum() collides")
	}
}

func BenchmarkLockKey(b *testing.B) {
	for b.Loop() {
		migrate.LockKey("app")
	}
}
