package migrate

import (
	"time"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Exposed internals.
func Split(sql string) []Piece                        { return split(sql) }
func LockKey(database string) string                  { return lockKey(database) }
func Spread(duration time.Duration) time.Duration     { return spread(duration) }
func Conflicting(mode safety.LockMode) string         { return conflicting(mode) }
func ConcurrentIndex(statement string) (string, bool) { return concurrentIndex(statement) }
func Milliseconds(duration time.Duration) string      { return milliseconds(duration) }
func TopLevelWhere(statement string) int              { return topLevelWhere(statement) }
func Locks(step *Step) []safety.RelLock               { return locks(step) }

// Schedule is the attempt timeouts and pauses for a first lock timeout.
func Schedule(first time.Duration) [][2]time.Duration {
	attempts := schedule(first)
	result := make([][2]time.Duration, 0, len(attempts))

	for _, attempt := range attempts {
		result = append(result, [2]time.Duration{attempt.timeout, attempt.pause})
	}

	return result
}

// StateOf exposes stateOf for a journal row's outcome and checksum.
func StateOf(outcome, recorded string, found bool, checksum string) State {
	return stateOf(entry{outcome: outcome, checksum: recorded}, found, checksum)
}

// SchemaStatements exposes schemaStatements.
func SchemaStatements(parser parse.Parser, files []catalog.File) ([]string, error) {
	return schemaStatements(parser, files)
}

// SplitUpdate exposes splitUpdate as table, head and condition.
func SplitUpdate(statement string) ([3]string, error) {
	parts, err := splitUpdate(statement)

	return [3]string{parts.table, parts.head, parts.condition}, err
}

// Validated exposes validated.
func Validated(statement string) [2]string {
	names, _ := validated(statement)

	return [2]string{names.first, names.second}
}

// Detached exposes detached.
func Detached(statement string) [2]string {
	names, _ := detached(statement)

	return [2]string{names.first, names.second}
}
