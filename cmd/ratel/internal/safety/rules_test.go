package safety_test

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// kinds is every operation kind but Unknown.
func kinds() []safety.Kind {
	return []safety.Kind{
		safety.CreateSchema, safety.DropSchema, safety.CreateExtension, safety.AlterExtension, safety.DropExtension,
		safety.CreateType, safety.DropType, safety.AddEnumValue, safety.RenameEnumValue, safety.RenameType,
		safety.AlterDomain,
		safety.DomainNotNull, safety.DomainCheck, safety.DomainCheckNotValid, safety.DomainValidate,
		safety.CreateSequence, safety.AlterSequence, safety.DropSequence, safety.CreateRoutine, safety.ReplaceRoutine,
		safety.DropRoutine, safety.CreateTable, safety.CreatePartition, safety.DropTable, safety.RenameTable,
		safety.Truncate, safety.SetLogged, safety.RewriteTable, safety.AddColumn, safety.DropColumn, safety.RenameColumn,
		safety.AlterColumnType, safety.SetDefault, safety.DropDefault, safety.SetNotNull, safety.SetNotNullChecked,
		safety.NotNullNotValid, safety.DropNotNull, safety.AlterIdentity, safety.SetExpression, safety.DropExpression,
		safety.AddCheck, safety.AddCheckNotValid, safety.AddForeignKey, safety.AddForeignKeyNotValid,
		safety.ValidateConstraint, safety.ValidateForeignKey, safety.AddUnique, safety.AddConstraintIndex,
		safety.AddExclusion, safety.DropConstraint, safety.RenameConstraint, safety.CreateIndex,
		safety.CreateIndexConcurrently, safety.CreateIndexOnly, safety.AttachIndex, safety.DropIndex,
		safety.DropIndexConcurrently, safety.RenameIndex, safety.Reindex, safety.ReindexConcurrently,
		safety.AttachPartition, safety.AttachPartitionChecked, safety.DetachPartition, safety.DetachConcurrently,
		safety.DetachPartitionFinalize, safety.CreateView, safety.ReplaceView, safety.DropView, safety.RefreshView,
		safety.CreateTrigger, safety.DropTrigger, safety.CreatePolicy, safety.DropPolicy, safety.AlterRLS,
		safety.Comment, safety.Grant, safety.Backfill, safety.DataChange,
	}
}

// TestEveryKindHasARule checks no kind falls through to the worst-case rule
// of an unknown statement.
func TestEveryKindHasARule(t *testing.T) {
	t.Parallel()

	env := existing("app.t", "app.u")
	unknown := safety.Assess([]safety.Operation{{Kind: safety.Unknown, Relation: "app.t"}}, env)

	if unknown.Destroys != safety.Data || unknown.Compat != safety.Breaking || unknown.Effect != safety.Rewrite {
		t.Fatalf("unknown statement = %+v", unknown)
	}

	for _, kind := range kinds() {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			got := safety.Assess([]safety.Operation{{Kind: kind, Relation: "app.t", Other: "app.u"}}, env)
			if cmp.Equal(got, unknown) {
				t.Errorf("%s has no rule: %+v", kind, got)
			}
		})
	}
}

// TestReasonCodes checks the lint code each operation on an existing table
// reports, as docs/migrations.md lists them.
func TestReasonCodes(t *testing.T) {
	t.Parallel()

	env := existing("app.t", "app.u")

	for _, tc := range []struct {
		kind safety.Kind
		code string
	}{
		{safety.Truncate, "RTL-D02"},
		{safety.DropIndex, "RTL-L03"},
		{safety.DropConstraint, "RTL-D04"},
		{safety.AddUnique, "RTL-L05"},
		{safety.DetachPartition, "RTL-L08"},
		{safety.AddExclusion, "RTL-L09"},
		{safety.CreateIndex, "RTL-L02"},
		{safety.Reindex, "RTL-L10"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()

			got := safety.Assess([]safety.Operation{{Kind: tc.kind, Relation: "app.t", Other: "app.u"}}, env)
			if !slices.Contains(got.Reasons, tc.code) {
				t.Errorf("%s reasons = %v, want %s", tc.kind, got.Reasons, tc.code)
			}
		})
	}
}

// TestNonBlockingLongDDL checks that every statement that cannot run in a
// transaction takes no lock blocking reads or writes.
func TestNonBlockingLongDDL(t *testing.T) {
	t.Parallel()

	env := existing("app.t", "app.u")

	for _, kind := range kinds() {
		got := safety.Assess([]safety.Operation{{Kind: kind, Relation: "app.t", Other: "app.u"}}, env)
		if got.Tx != migrate.OwnStatement {
			continue
		}

		for _, lock := range got.Locks {
			if lock.Mode.Blocks() {
				t.Errorf("%s takes %s on %s", kind, lock.Mode, lock.Relation)
			}
		}
	}
}

func BenchmarkRules(b *testing.B) {
	env := existing("app.t", "app.u")
	operations := make([]safety.Operation, 0, len(kinds()))

	for _, kind := range kinds() {
		operations = append(operations, safety.Operation{Kind: kind, Relation: "app.t", Other: "app.u"})
	}

	for b.Loop() {
		safety.Assess(operations, env)
	}
}
