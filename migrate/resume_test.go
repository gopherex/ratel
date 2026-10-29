package migrate_test

import (
	"testing"

	"github.com/gopherex/ratel/migrate"
)

func TestConcurrentIndex(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		statement, want string
		ok              bool
	}{
		{"CREATE UNIQUE INDEX CONCURRENTLY i ON ONLY app.t (a)", "app.i", true},
		{"CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON t (a)", "public.i", true},
		{"CREATE INDEX i ON app.t (a)", "", false},
		{"DROP INDEX CONCURRENTLY app.i", "", false},
	} {
		t.Run(tc.statement, func(t *testing.T) {
			t.Parallel()

			if got, ok := migrate.ConcurrentIndex(tc.statement); got != tc.want || ok != tc.ok {
				t.Errorf("concurrentIndex() = %q, %v", got, ok)
			}
		})
	}
}

func TestResumedShapes(t *testing.T) {
	t.Parallel()

	if got := migrate.Validated("ALTER TABLE app.t VALIDATE CONSTRAINT c"); got != [2]string{"app.t", "c"} {
		t.Errorf("validated() = %v", got)
	}

	if got := migrate.Validated("SELECT 1"); got != [2]string{} {
		t.Errorf("validated() of a select = %v", got)
	}

	if got := migrate.Detached("ALTER TABLE app.p DETACH PARTITION app.c CONCURRENTLY"); got != [2]string{
		"app.p", "app.c",
	} {
		t.Errorf("detached() = %v", got)
	}

	if got := migrate.Detached("ALTER TABLE app.p DETACH PARTITION app.c"); got != [2]string{} {
		t.Errorf("blocking detach = %v", got)
	}
}

func BenchmarkConcurrentIndex(b *testing.B) {
	for b.Loop() {
		migrate.ConcurrentIndex("CREATE UNIQUE INDEX CONCURRENTLY i ON ONLY app.t (a)")
	}
}
