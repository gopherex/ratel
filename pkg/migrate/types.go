package migrate

// Phase is when a step runs: expand and backfill with Apply, contract with
// Contract.
type Phase string

// Phases in run order.
const (
	PhaseExpand   Phase = "expand"
	PhaseBackfill Phase = "backfill"
	PhaseContract Phase = "contract"
)

// Class is how the runner guards a step.
type Class string

// Step classes.
const (
	// ClassShort takes a blocking lock briefly, under lock_timeout and
	// retries.
	ClassShort Class = "short"
	// ClassLong scans or builds without a blocking lock and may wait for
	// older transactions.
	ClassLong Class = "long"
	// ClassBatched fills rows in key-ordered batches, one transaction each.
	ClassBatched Class = "backfill"
)

// TxMode is whether a statement can run inside a transaction.
type TxMode int

// Transaction modes.
const (
	InTx TxMode = iota
	OwnStatement
)

// LockMode is a PostgreSQL table lock mode, weakest first.
type LockMode int

// Lock modes in the order of pg_locks conflicts.
const (
	NoLock LockMode = iota
	AccessShare
	RowShare
	RowExclusive
	ShareUpdateExclusive
	Share
	ShareRowExclusive
	Exclusive
	AccessExclusive
)

// String is the mode as pg_locks names it without the "Lock" suffix.
func (m LockMode) String() string {
	switch m {
	case AccessShare:
		return "AccessShare"
	case RowShare:
		return "RowShare"
	case RowExclusive:
		return "RowExclusive"
	case ShareUpdateExclusive:
		return "ShareUpdateExclusive"
	case Share:
		return "Share"
	case ShareRowExclusive:
		return "ShareRowExclusive"
	case Exclusive:
		return "Exclusive"
	case AccessExclusive:
		return "AccessExclusive"
	default:
		return "None"
	}
}

// Blocks reports whether the mode blocks ordinary reads or writes: anything
// stronger than ShareUpdateExclusive conflicts with RowExclusive.
func (m LockMode) Blocks() bool { return m > ShareUpdateExclusive }

// Conflicts reports whether two modes cannot be held on one relation by two
// transactions, by PostgreSQL's table-level lock conflict table.
func (m LockMode) Conflicts(other LockMode) bool {
	if m == NoLock || other == NoLock {
		return false
	}

	low, high := min(m, other), max(m, other)

	switch low {
	case AccessShare:
		return high == AccessExclusive
	case RowShare:
		return high >= Exclusive
	case RowExclusive:
		return high >= Share
	case ShareUpdateExclusive:
		return high >= ShareUpdateExclusive
	case Share:
		return high != Share
	default:
		// ShareRowExclusive and stronger conflict with themselves and above.
		return true
	}
}

// RelLock is a lock on one relation.
type RelLock struct {
	Relation string // "schema.name"
	Mode     LockMode
}
