package migrate_test

import (
	"testing"
	"time"

	"github.com/gopherex/ratel/migrate"
)

func TestSchedule(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		first time.Duration
		want  map[int][2]time.Duration
	}{
		{first: 100 * time.Millisecond, want: map[int][2]time.Duration{
			0: {100 * time.Millisecond, 50 * time.Millisecond}, 10: {time.Second, 15 * time.Second},
			40: {2 * time.Second, 15 * time.Second}, 41: {100 * time.Millisecond, 50 * time.Millisecond},
		}},
		{first: 200 * time.Millisecond, want: map[int][2]time.Duration{
			2: {400 * time.Millisecond, 50 * time.Millisecond}, 5: {time.Second, 2 * time.Second},
		}},
	} {
		t.Run(tc.first.String(), func(t *testing.T) {
			t.Parallel()

			attempts := migrate.Schedule(tc.first)
			if len(attempts) != 41*15 {
				t.Fatalf("attempts = %d", len(attempts))
			}

			for index, want := range tc.want {
				if attempts[index] != want {
					t.Errorf("attempt %d = %v, want %v", index, attempts[index], want)
				}
			}
		})
	}
}

func BenchmarkSchedule(b *testing.B) {
	for b.Loop() {
		migrate.Schedule(100 * time.Millisecond)
	}
}
