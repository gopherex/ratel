package safety

// Kind is one thing a statement does. The lock and effect table is keyed by
// it; tests replay every kind against PostgreSQL.
type Kind string

// Operation kinds.
const (
	CreateSchema    Kind = "create-schema"
	DropSchema      Kind = "drop-schema"
	CreateExtension Kind = "create-extension"
	AlterExtension  Kind = "alter-extension"
	DropExtension   Kind = "drop-extension"

	CreateType      Kind = "create-type"
	DropType        Kind = "drop-type"
	AddEnumValue    Kind = "add-enum-value"
	RenameEnumValue Kind = "rename-enum-value"
	RenameType      Kind = "rename-type"
	AlterDomain     Kind = "alter-domain" // default, DROP NOT NULL, drop constraint
	DomainNotNull   Kind = "domain-set-not-null"
	DomainCheck     Kind = "domain-add-check"
	// DomainCheckNotValid adds a domain check without checking stored values.
	DomainCheckNotValid Kind = "domain-add-check-not-valid"
	DomainValidate      Kind = "domain-validate-constraint"

	CreateSequence Kind = "create-sequence"
	AlterSequence  Kind = "alter-sequence"
	DropSequence   Kind = "drop-sequence"

	CreateRoutine  Kind = "create-routine"
	ReplaceRoutine Kind = "replace-routine"
	DropRoutine    Kind = "drop-routine"

	// CreateTable creates Relation; Other is a table its foreign keys
	// reference.
	CreateTable Kind = "create-table"
	// CreatePartition creates partition Other of the existing Relation.
	CreatePartition Kind = "create-partition"
	DropTable       Kind = "drop-table"
	RenameTable     Kind = "rename-table"
	Truncate        Kind = "truncate"
	SetLogged       Kind = "set-logged" // SET LOGGED or SET UNLOGGED
	// RewriteTable is SET ACCESS METHOD, SET TABLESPACE, CLUSTER or VACUUM
	// FULL.
	RewriteTable Kind = "rewrite-table"

	AddColumn       Kind = "add-column"
	DropColumn      Kind = "drop-column"
	RenameColumn    Kind = "rename-column"
	AlterColumnType Kind = "alter-column-type"
	SetDefault      Kind = "set-default"
	DropDefault     Kind = "drop-default"
	SetNotNull      Kind = "set-not-null"
	// SetNotNullChecked is SET NOT NULL over a validated IS NOT NULL check,
	// which PostgreSQL trusts instead of scanning.
	SetNotNullChecked Kind = "set-not-null-checked"
	NotNullNotValid   Kind = "add-not-null-not-valid" // PostgreSQL 18
	DropNotNull       Kind = "drop-not-null"
	AlterIdentity     Kind = "alter-identity"
	SetExpression     Kind = "set-expression"
	DropExpression    Kind = "drop-expression"

	AddCheck              Kind = "add-check"
	AddCheckNotValid      Kind = "add-check-not-valid"
	AddForeignKey         Kind = "add-foreign-key"
	AddForeignKeyNotValid Kind = "add-foreign-key-not-valid"
	ValidateConstraint    Kind = "validate-constraint"
	ValidateForeignKey    Kind = "validate-foreign-key"
	AddUnique             Kind = "add-unique" // UNIQUE or PRIMARY KEY building its index
	AddConstraintIndex    Kind = "add-constraint-using-index"
	AddExclusion          Kind = "add-exclusion"
	DropConstraint        Kind = "drop-constraint"
	RenameConstraint      Kind = "rename-constraint"

	CreateIndex             Kind = "create-index"
	CreateIndexConcurrently Kind = "create-index-concurrently"
	CreateIndexOnly         Kind = "create-index-on-only" // partitioned parent, invalid until attached
	// AttachIndex and RenameIndex act on the index named by Relation.
	AttachIndex           Kind = "attach-index"
	DropIndex             Kind = "drop-index"
	DropIndexConcurrently Kind = "drop-index-concurrently"
	RenameIndex           Kind = "rename-index"
	Reindex               Kind = "reindex"
	ReindexConcurrently   Kind = "reindex-concurrently"

	AttachPartition Kind = "attach-partition"
	// AttachPartitionChecked attaches over a validated check matching the
	// bound, which PostgreSQL trusts instead of scanning.
	AttachPartitionChecked  Kind = "attach-partition-checked"
	DetachPartition         Kind = "detach-partition"
	DetachConcurrently      Kind = "detach-partition-concurrently"
	DetachPartitionFinalize Kind = "detach-partition-finalize"

	CreateView    Kind = "create-view"
	ReplaceView   Kind = "replace-view"
	DropView      Kind = "drop-view"
	RefreshView   Kind = "refresh-materialized-view"
	CreateTrigger Kind = "create-trigger"
	DropTrigger   Kind = "drop-trigger"
	CreatePolicy  Kind = "create-policy"
	DropPolicy    Kind = "drop-policy"
	AlterRLS      Kind = "alter-row-security"

	Comment Kind = "comment"
	Grant   Kind = "grant" // GRANT or REVOKE

	Backfill   Kind = "backfill"    // one batch of a backfill step
	DataChange Kind = "data-change" // INSERT, UPDATE, DELETE or MERGE
	Unknown    Kind = "unknown"     // a statement the classifier does not know
)

// Operation is one thing a statement does to one relation.
type Operation struct {
	Kind Kind
	// Relation is the table, view or sequence the operation acts on, as
	// "schema.name"; empty for objects that are not relations.
	Relation string
	// Other is the second relation: the table a foreign key references, the
	// partition attached or detached.
	Other string
	// Rewrite marks a statement found to rewrite the table: a type change or
	// a column added with a volatile default, by the dev server probe.
	Rewrite bool
	// Codes are lint codes the classifier found in the statement itself,
	// such as NOT NULL without a default.
	Codes []string
}
