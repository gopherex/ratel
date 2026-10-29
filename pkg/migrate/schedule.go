package migrate

import (
	"time"
)

// attempt is one try of a short step: its lock_timeout and the pause after
// it times out.
type attempt struct{ timeout, pause time.Duration }

// gitlabRepeats is how often GitLab repeats its with_lock_retries table.
const gitlabRepeats = 15

// gitlabBase is the first lock_timeout of the GitLab table; a configured
// lock_timeout scales every timeout of the table by its ratio to this.
const gitlabBase = 100 * time.Millisecond

// schedule is GitLab's Database::WithLockRetries DEFAULT_TIMING_CONFIGURATION
// without its final attempt without a timeout, its lock timeouts scaled to
// the configured first one. max_total_wait bounds the run through it.
func schedule(first time.Duration) []attempt {
	const (
		ms100 = 100 * time.Millisecond
		ms200 = 200 * time.Millisecond
		ms300 = 300 * time.Millisecond
		ms400 = 400 * time.Millisecond
		ms500 = 500 * time.Millisecond
		ms50  = 50 * time.Millisecond
		ms150 = 150 * time.Millisecond
	)

	table := []attempt{
		{ms100, ms50},
		{ms100, ms50},
		{ms200, ms50},
		{ms300, ms100},
		{ms400, ms150},
		{ms500, 2 * time.Second},
		{ms500, 2 * time.Second},
		{ms500, 2 * time.Second},
		{ms500, 2 * time.Second},
		{time.Second, 5 * time.Second},
		{time.Second, 15 * time.Second},
		{ms100, ms50},
		{ms100, ms50},
		{ms200, ms50},
		{ms300, ms100},
		{ms400, ms150},
		{ms500, 2 * time.Second},
		{ms500, 2 * time.Second},
		{ms500, 2 * time.Second},
		{2 * time.Second, 15 * time.Second},
		{ms100, ms50},
		{ms100, ms50},
		{ms500, 2 * time.Second},
		{ms500, 2 * time.Second},
		{2 * time.Second, 15 * time.Second},
		{ms500, ms500},
		{ms500, ms500},
		{2 * time.Second, 15 * time.Second},
		{ms500, ms500},
		{ms500, ms500},
		{2 * time.Second, 15 * time.Second},
		{ms500, ms500},
		{ms500, ms500},
		{2 * time.Second, 15 * time.Second},
		{ms100, ms50},
		{ms100, ms50},
		{ms500, 2 * time.Second},
		{2 * time.Second, 15 * time.Second},
		{ms100, ms50},
		{ms500, 2 * time.Second},
		{2 * time.Second, 15 * time.Second},
	}

	result := make([]attempt, 0, len(table)*gitlabRepeats)
	scale := float64(first) / float64(gitlabBase)

	for range gitlabRepeats {
		for _, entry := range table {
			result = append(result, attempt{timeout: time.Duration(float64(entry.timeout) * scale), pause: entry.pause})
		}
	}

	return result
}
