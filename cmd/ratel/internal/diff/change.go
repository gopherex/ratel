package diff

// Kind is a change kind. The planner keys its recipes and the lock and effect
// table by it.
type Kind string

// Change kinds, one per recipe row of docs/migrations.md.
const (
	CreateSchema    Kind = "create schema"
	DropSchema      Kind = "drop schema"
	CreateExtension Kind = "create extension"
	AlterExtension  Kind = "alter extension"
	DropExtension   Kind = "drop extension"

	CreateType      Kind = "create type"
	DropType        Kind = "drop type"
	AddEnumValue    Kind = "add enum value"
	RenameEnumValue Kind = "rename enum value"
	// RemoveEnumValue removes or reorders labels; no PostgreSQL statement
	// exists, so the change carries no SQL.
	RemoveEnumValue      Kind = "remove enum value"
	AlterDomain          Kind = "alter domain" // default, DROP NOT NULL
	SetDomainNotNull     Kind = "set domain not null"
	AddDomainCheck       Kind = "add domain check"
	DropDomainConstraint Kind = "drop domain constraint"

	CreateSequence Kind = "create sequence"
	AlterSequence  Kind = "alter sequence"
	DropSequence   Kind = "drop sequence"

	CreateRoutine  Kind = "create routine"
	ReplaceRoutine Kind = "replace routine" // same signature and result: CREATE OR REPLACE
	DropRoutine    Kind = "drop routine"

	CreateTable      Kind = "create table"
	DropTable        Kind = "drop table"
	RenameTable      Kind = "rename table"
	AlterPersistence Kind = "alter persistence"
	AttachPartition  Kind = "attach partition"
	DetachPartition  Kind = "detach partition"

	AddColumn       Kind = "add column"
	RenameColumn    Kind = "rename column"
	DropColumn      Kind = "drop column"
	AlterColumnType Kind = "alter column type"
	SetNotNull      Kind = "set not null"
	DropNotNull     Kind = "drop not null"
	SetDefault      Kind = "set default"
	DropDefault     Kind = "drop default"
	AlterIdentity   Kind = "alter identity"
	AlterGenerated  Kind = "alter generated"
	AlterCollation  Kind = "alter collation"

	AddCheck         Kind = "add check"
	AddForeignKey    Kind = "add foreign key"
	AddUnique        Kind = "add unique"
	AddPrimaryKey    Kind = "add primary key"
	AddExclusion     Kind = "add exclusion"
	DropConstraint   Kind = "drop constraint"
	RenameConstraint Kind = "rename constraint" // same definition, new name

	CreateIndex Kind = "create index"
	DropIndex   Kind = "drop index"
	RenameIndex Kind = "rename index" // same definition, new name

	CreateView  Kind = "create view"
	ReplaceView Kind = "replace view" // columns only appended: CREATE OR REPLACE VIEW
	DropView    Kind = "drop view"

	CreateTrigger Kind = "create trigger"
	DropTrigger   Kind = "drop trigger"
	CreatePolicy  Kind = "create policy"
	DropPolicy    Kind = "drop policy"
	EnableRLS     Kind = "enable row level security"
	DisableRLS    Kind = "disable row level security"

	Grant   Kind = "grant"
	Revoke  Kind = "revoke"
	Comment Kind = "comment"
)

// Change is one step from the current state toward the desired one.
type Change struct {
	Kind Kind
	// Key is the object's rendering key ("table app.users"); a column change
	// is keyed "column app.users.email", an enum label change "value
	// app.status 'label'".
	Key string
	// Table is the "schema.table" whose lock the change takes, when it takes
	// one.
	Table string
	// SQL makes the change as plain PostgreSQL; the planner rewrites it into
	// safe steps.
	SQL string
	// Before and After are the old and new definitions the change goes
	// between: a column type, a default, a constraint or index definition.
	Before, After string
	// Column is the column of a column change.
	Column string
}
