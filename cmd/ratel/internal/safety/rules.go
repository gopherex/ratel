package safety

// Lint codes the table gives operations; docs/migrations.md lists them all.
const (
	codeRename       = "RTL-C01"
	codeDropInUse    = "RTL-C02"
	codeNotNull      = "RTL-C03"
	codeRetype       = "RTL-C04"
	codeEnumValue    = "RTL-C05"
	codeTightening   = "RTL-C06"
	codeLoosening    = "RTL-C07"
	codeDropData     = "RTL-D01"
	codeTruncate     = "RTL-D02"
	codeDropObject   = "RTL-D04"
	codeIndexBuild   = "RTL-L02"
	codeIndexDrop    = "RTL-L03"
	codeValidation   = "RTL-L04"
	codeUniqueBuild  = "RTL-L05"
	codeNotNullScan  = "RTL-L06"
	codeAttachScan   = "RTL-L07"
	codeDetach       = "RTL-L08"
	codeExclusion    = "RTL-L09"
	codeReindex      = "RTL-L10"
	codeFillRewrite  = "RTL-R01"
	codeTypeRewrite  = "RTL-R02"
	codeTableRewrite = "RTL-R03"
	codeDataChange   = "RTL-T06"
)

// rule is the lock and effect table entry of a kind.
type rule struct {
	mode, other LockMode // on Relation and on Other
	effect      Effect
	tx          TxMode
	destroys    Destroys
	compat      Compat
	waits       bool
	codes       []string
	rewrite     string // the code a probed rewrite adds
}

func (r rule) locks(operation Operation) []RelLock {
	return []RelLock{{operation.Relation, r.mode}, {operation.Other, r.other}}
}

// metadata is a rule taking a lock for a catalog change only.
func metadata(mode LockMode, codes ...string) rule { return rule{mode: mode, codes: codes} }

// tightening is a rule adding a constraint that existing rows or old code
// may violate.
func tightening(mode LockMode, effect Effect, codes ...string) rule {
	return rule{mode: mode, effect: effect, compat: Conditional, codes: append([]string{codeTightening}, codes...)}
}

// dropping is a rule removing something running code may still use.
func dropping(mode LockMode, destroys Destroys, codes ...string) rule {
	return rule{
		mode: mode, destroys: destroys, compat: Breaking, codes: append([]string{codeDropInUse}, codes...),
	}
}

// concurrent is a rule for a statement that cannot run in a transaction and
// waits for older ones.
func concurrent(mode, other LockMode, effect Effect) rule {
	return rule{mode: mode, other: other, effect: effect, tx: OwnStatement, waits: true}
}

// grantLocks is the first major version whose GRANT locks the relation.
const grantLocks = 18

// ruleOf is the table entry of an operation's kind on a major version.
func ruleOf(kind Kind, major int) rule {
	if kind == Grant && major >= grantLocks {
		return metadata(AccessShare)
	}

	for _, table := range []func(Kind) (rule, bool){
		objectRule, columnRule, constraintRule, indexRule, tableRule, dependentRule,
	} {
		if entry, ok := table(kind); ok {
			return entry
		}
	}

	// An unknown statement is assumed to do the worst.
	return rule{mode: AccessExclusive, effect: Rewrite, destroys: Data, compat: Breaking}
}

// objectRule covers schemas, extensions, types, sequences and routines.
func objectRule(kind Kind) (rule, bool) {
	switch kind {
	case CreateSchema, CreateExtension, AlterExtension, CreateType, RenameEnumValue, AlterDomain,
		CreateRoutine, ReplaceRoutine, DomainCheckNotValid:
		return metadata(NoLock), true
	case DropSchema:
		return dropping(NoLock, Data, codeDropData), true
	case DropExtension, DropType, DropRoutine:
		return dropping(NoLock, Objects), true
	case AddEnumValue:
		return rule{compat: Conditional, codes: []string{codeEnumValue}}, true
	case DomainNotNull, DomainCheck:
		return tightening(NoLock, Scan, codeNotNullScan), true
	case DomainValidate:
		return tightening(NoLock, Scan), true
	case CreateSequence:
		return metadata(AccessExclusive), true
	case AlterSequence:
		return metadata(ShareRowExclusive), true
	case DropSequence:
		return dropping(AccessExclusive, Objects), true
	default:
		return rule{}, false
	}
}

func columnRule(kind Kind) (rule, bool) {
	switch kind {
	case AddColumn, SetExpression:
		return rule{mode: AccessExclusive, rewrite: codeFillRewrite}, true
	case DropColumn:
		return dropping(AccessExclusive, Data, codeDropData), true
	case RenameColumn:
		return rule{mode: AccessExclusive, compat: Breaking, codes: []string{codeRename}}, true
	case AlterColumnType:
		return rule{mode: AccessExclusive, compat: Breaking, codes: []string{codeRetype}, rewrite: codeTypeRewrite}, true
	case SetDefault, AlterIdentity, SetNotNullChecked, NotNullNotValid, DropExpression:
		return metadata(AccessExclusive), true
	case DropDefault:
		return rule{
			mode: AccessExclusive, destroys: Objects, compat: Conditional, codes: []string{codeLoosening, codeDropObject},
		}, true
	case SetNotNull:
		return tightening(AccessExclusive, Scan, codeNotNullScan), true
	case DropNotNull:
		return rule{mode: AccessExclusive, compat: Conditional, codes: []string{codeLoosening}}, true
	default:
		return rule{}, false
	}
}

func constraintRule(kind Kind) (rule, bool) {
	switch kind {
	case AddCheck:
		return tightening(AccessExclusive, Scan, codeValidation), true
	case AddCheckNotValid, AddConstraintIndex:
		return tightening(AccessExclusive, Metadata), true
	case AddForeignKey:
		entry := tightening(ShareRowExclusive, Scan, codeValidation)
		entry.other = ShareRowExclusive

		return entry, true
	case AddForeignKeyNotValid:
		entry := tightening(ShareRowExclusive, Metadata)
		entry.other = ShareRowExclusive

		return entry, true
	case ValidateConstraint:
		return rule{mode: ShareUpdateExclusive, effect: Scan}, true
	case ValidateForeignKey:
		return rule{mode: ShareUpdateExclusive, other: RowShare, effect: Scan}, true
	case AddUnique:
		return tightening(AccessExclusive, IndexBuild, codeUniqueBuild), true
	case AddExclusion:
		return tightening(AccessExclusive, IndexBuild, codeExclusion), true
	case DropConstraint:
		return rule{mode: AccessExclusive, destroys: Objects, codes: []string{codeDropObject}}, true
	case RenameConstraint:
		return metadata(AccessExclusive), true
	default:
		return rule{}, false
	}
}

func indexRule(kind Kind) (rule, bool) {
	switch kind {
	case CreateIndex:
		return rule{mode: Share, effect: IndexBuild, codes: []string{codeIndexBuild}}, true
	case CreateIndexConcurrently, ReindexConcurrently:
		return concurrent(ShareUpdateExclusive, NoLock, IndexBuild), true
	case CreateIndexOnly:
		return metadata(Share), true
	case AttachIndex, RenameIndex:
		return metadata(ShareUpdateExclusive), true
	case DropIndex:
		return rule{mode: AccessExclusive, destroys: Objects, codes: []string{codeDropObject, codeIndexDrop}}, true
	case DropIndexConcurrently:
		entry := concurrent(ShareUpdateExclusive, NoLock, Metadata)
		entry.destroys, entry.codes = Objects, []string{codeDropObject}

		return entry, true
	case Reindex:
		return rule{mode: Share, effect: IndexBuild, codes: []string{codeReindex}}, true
	default:
		return rule{}, false
	}
}

// tableRule covers tables and partitions.
func tableRule(kind Kind) (rule, bool) {
	switch kind {
	case CreateTable:
		return rule{mode: AccessExclusive, other: ShareRowExclusive}, true
	case CreatePartition:
		return rule{mode: AccessExclusive, other: AccessExclusive}, true
	case DropTable:
		return dropping(AccessExclusive, Data, codeDropData), true
	case RenameTable:
		return rule{mode: AccessExclusive, compat: Breaking, codes: []string{codeRename}}, true
	case Truncate:
		// TRUNCATE swaps in an empty file: a rewrite that reads nothing.
		return rule{
			mode: AccessExclusive, effect: Rewrite, destroys: Data, compat: Breaking, codes: []string{codeTruncate},
		}, true
	case SetLogged, RewriteTable:
		return rule{mode: AccessExclusive, effect: Rewrite, codes: []string{codeTableRewrite}}, true
	case AttachPartition:
		return rule{mode: ShareUpdateExclusive, other: AccessExclusive, effect: Scan, codes: []string{codeAttachScan}}, true
	case AttachPartitionChecked:
		return rule{mode: ShareUpdateExclusive, other: AccessExclusive}, true
	case DetachPartition:
		return rule{mode: AccessExclusive, other: AccessExclusive, codes: []string{codeDetach}}, true
	case DetachConcurrently, DetachPartitionFinalize:
		return concurrent(ShareUpdateExclusive, ShareUpdateExclusive, Metadata), true
	default:
		return rule{}, false
	}
}

// dependentRule covers views, triggers, policies, comments, grants and data.
func dependentRule(kind Kind) (rule, bool) {
	switch kind {
	case CreateView, ReplaceView, CreatePolicy, DropPolicy, AlterRLS:
		return metadata(AccessExclusive), true
	case DropView:
		return dropping(AccessExclusive, Objects), true
	case RefreshView:
		return rule{mode: AccessExclusive, effect: Rewrite}, true
	case CreateTrigger:
		return metadata(ShareRowExclusive), true
	case DropTrigger:
		return rule{mode: AccessExclusive, destroys: Objects}, true
	case Comment:
		return metadata(ShareUpdateExclusive), true
	case Grant:
		return metadata(NoLock), true
	case Backfill:
		return metadata(RowExclusive), true
	case DataChange:
		return metadata(RowExclusive, codeDataChange), true
	default:
		return rule{}, false
	}
}
