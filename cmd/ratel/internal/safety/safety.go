// Package safety assesses what a migration statement does to a running
// database: the locks it takes and on what, whether it scans, rewrites or
// builds an index under them, whether it can run in a transaction, what it
// destroys and whether running application code notices.
//
// A statement is a list of operations. The planner states the operations of
// each step it writes; a hand-written statement is classified from its parse
// tree. Assess is pure: facts that need a database (rewrites, partitions)
// come in through Env.
package safety

import (
	"cmp"
	"slices"
	"strings"

	"github.com/gopherex/ratel/migrate"
)

// Effect is the heaviest work a statement does under its locks.
type Effect int

// Effects, cheapest first.
const (
	Metadata Effect = iota
	Scan
	IndexBuild
	Rewrite
)

func (e Effect) String() string {
	switch e {
	case Scan:
		return "scan"
	case IndexBuild:
		return "index build"
	case Rewrite:
		return "rewrite"
	default:
		return "metadata"
	}
}

// Destroys is what a statement removes irrecoverably.
type Destroys int

// Destruction levels.
const (
	None Destroys = iota
	Objects
	Data
)

// Compat is how running application code sees a statement.
type Compat int

// Compatibility levels.
const (
	Safe Compat = iota
	Conditional
	Breaking
)

// Safety is the assessment of one statement.
type Safety struct {
	Locks    []migrate.RelLock // strictest first, one per relation
	Effect   Effect
	Tx       migrate.TxMode
	Destroys Destroys
	Compat   Compat
	Reasons  []string // lint codes behind Effect, Destroys and Compat, in rule order
	Waits    bool     // waits for older transactions
}

// Relation is what Env knows of an existing relation.
type Relation struct {
	Partitioned      bool
	DefaultPartition bool
	PartitionKey     []string
	Partitions       []string // "schema.name" of each partition, in name order
}

// Env is the target the statements run against.
type Env struct {
	Major     int                 // target major version
	Relations map[string]Relation // existing relations by "schema.name"
	// Created are relations created earlier in the same plan; their locks
	// block nobody.
	Created map[string]bool
	// Indexes are the tables of existing indexes, by "schema.index".
	Indexes map[string]string
}

// Exists reports whether a relation exists on the target before the plan.
func (e Env) Exists(relation string) bool {
	_, exists := e.Relations[relation]

	return exists && !e.Created[relation]
}

// Assess combines the operations of one statement.
func Assess(operations []Operation, env Env) Safety {
	var result Safety

	modes := make(map[string]migrate.LockMode)

	for index := range operations {
		operation := &operations[index]
		entry := ruleOf(operation.Kind, env.Major)
		entry.codes = append(slices.Clone(entry.codes), operation.Codes...)

		if operation.Rewrite && entry.rewrite != "" {
			entry.effect, entry.codes = Rewrite, append(entry.codes, entry.rewrite)
		}

		for _, lock := range entry.locks(*operation) {
			if lock.Relation != "" && lock.Mode != migrate.NoLock && env.Exists(lock.Relation) {
				modes[lock.Relation] = max(modes[lock.Relation], lock.Mode)
			}
		}

		// Work on a relation created in the same plan blocks nobody.
		if operation.Relation != "" && !env.Exists(operation.Relation) {
			entry.effect, entry.codes = Metadata, exempt(entry.codes)
		}

		result.Effect = max(result.Effect, entry.effect)
		result.Destroys = max(result.Destroys, entry.destroys)
		result.Compat = max(result.Compat, entry.compat)
		result.Waits = result.Waits || entry.waits

		if entry.tx == migrate.OwnStatement {
			result.Tx = migrate.OwnStatement
		}

		for _, code := range entry.codes {
			if !slices.Contains(result.Reasons, code) {
				result.Reasons = append(result.Reasons, code)
			}
		}
	}

	for relation, mode := range modes {
		result.Locks = append(result.Locks, migrate.RelLock{Relation: relation, Mode: mode})
	}

	slices.SortFunc(result.Locks, func(a, b migrate.RelLock) int {
		return cmp.Or(cmp.Compare(b.Mode, a.Mode), cmp.Compare(a.Relation, b.Relation))
	})
	slices.Sort(result.Reasons)

	return result
}

// exempt drops the lock and rewrite codes, which do not apply to a relation
// nobody uses yet.
func exempt(codes []string) []string {
	return slices.DeleteFunc(slices.Clone(codes), func(code string) bool {
		return strings.HasPrefix(code, "RTL-L") || strings.HasPrefix(code, "RTL-R")
	})
}
