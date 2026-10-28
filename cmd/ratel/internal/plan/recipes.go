package plan

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// change dispatches a change to its recipe.
func (p *planner) change(change *diff.Change) {
	switch {
	case p.object(change):
	case p.constraint(change):
	case p.index(change):
	case p.column(change):
	default:
		p.relation(change)
	}
}

// object plans changes to objects that are not relations: they take no
// relation lock and are short DDL.
func (p *planner) object(change *diff.Change) bool {
	switch kind, ok := plainObject(change.Kind); {
	case ok:
		p.short(change, statement(change.SQL, kind, "", ""))
	case change.Kind == diff.RenameEnumValue:
		// Renamed by intent, before the values added next to it.
		p.add(change, Expand, Short, statement(change.SQL, safety.RenameEnumValue, "", ""))
		p.steps[len(p.steps)-1].Allow = append(p.steps[len(p.steps)-1].Allow, "RTL-C01")
	case change.Kind == diff.AddEnumValue:
		// A value is usable only after its transaction commits: its own step.
		p.add(change, Expand, Short, statement(change.SQL, safety.AddEnumValue, "", ""))
	case change.Kind == diff.RemoveEnumValue:
		p.unsafe(change, "RTL-D05", "PostgreSQL cannot drop or reorder enum values; write a hand-written migration")
	case change.Kind == diff.SetDomainNotNull:
		p.unsafe(change, "RTL-L06", "SET NOT NULL scans every column of the domain under a lock",
			statement(change.SQL, safety.DomainNotNull, "", ""))
	case change.Kind == diff.AddDomainCheck:
		domain, _, _ := strings.Cut(strings.TrimPrefix(change.SQL, "ALTER DOMAIN "), " ")
		name := checkName(change.SQL, " ADD CONSTRAINT ")

		p.add(change, Expand, Short, statement(strings.TrimSuffix(change.SQL, ";")+" NOT VALID;",
			safety.DomainCheckNotValid, "", ""))
		p.add(change, Contract, Long, statement("ALTER DOMAIN "+domain+" VALIDATE CONSTRAINT "+name+";",
			safety.DomainValidate, "", ""))
	case change.Kind == diff.CreateSequence, change.Kind == diff.AlterSequence, change.Kind == diff.DropSequence:
		p.short(change, statement(change.SQL, sequenceKind(change.Kind), nameOf(change.Key), ""))
	default:
		return false
	}

	return true
}

// plainObject is the operation of a change applied as written.
func plainObject(kind diff.Kind) (safety.Kind, bool) {
	switch kind {
	case diff.CreateSchema:
		return safety.CreateSchema, true
	case diff.DropSchema:
		return safety.DropSchema, true
	case diff.CreateExtension:
		return safety.CreateExtension, true
	case diff.AlterExtension:
		return safety.AlterExtension, true
	case diff.DropExtension:
		return safety.DropExtension, true
	case diff.CreateType:
		return safety.CreateType, true
	case diff.DropType:
		return safety.DropType, true
	case diff.AlterDomain, diff.DropDomainConstraint:
		return safety.AlterDomain, true
	case diff.CreateRoutine:
		return safety.CreateRoutine, true
	case diff.ReplaceRoutine:
		return safety.ReplaceRoutine, true
	case diff.DropRoutine:
		return safety.DropRoutine, true
	case diff.Comment:
		return safety.Comment, true
	case diff.Grant, diff.Revoke:
		return safety.Grant, true
	default:
		return safety.Unknown, false
	}
}

func sequenceKind(kind diff.Kind) safety.Kind {
	switch kind {
	case diff.CreateSequence:
		return safety.CreateSequence
	case diff.AlterSequence:
		return safety.AlterSequence
	default:
		return safety.DropSequence
	}
}

// relation plans tables, partitions, views, triggers, policies and row
// security.
func (p *planner) relation(change *diff.Change) {
	name := nameOf(change.Key)

	switch change.Kind {
	case diff.CreateTable:
		p.short(change, statement(change.SQL, safety.CreateTable, name, ""))
	case diff.DropTable:
		p.short(change, statement(change.SQL, safety.DropTable, name, ""))
	case diff.RenameTable:
		p.renameTable(change)
	case diff.AlterPersistence:
		p.unsafe(change, "RTL-R03", "SET LOGGED and SET UNLOGGED rewrite the table under an exclusive lock",
			statement(change.SQL, safety.SetLogged, change.Table, ""))
	case diff.AttachPartition:
		p.attach(change)
	case diff.DetachPartition:
		p.detach(change)
	case diff.CreateView:
		p.short(change, statement(change.SQL, safety.CreateView, name, ""))
	case diff.ReplaceView:
		p.short(change, statement(change.SQL, safety.ReplaceView, name, ""))
	case diff.DropView:
		p.short(change, statement(change.SQL, safety.DropView, name, ""))
	case diff.CreateTrigger:
		p.short(change, statement(change.SQL, safety.CreateTrigger, change.Table, ""))
	case diff.DropTrigger:
		p.short(change, statement(change.SQL, safety.DropTrigger, change.Table, ""))
	case diff.CreatePolicy:
		p.short(change, statement(change.SQL, safety.CreatePolicy, change.Table, ""))
	case diff.DropPolicy:
		p.short(change, statement(change.SQL, safety.DropPolicy, change.Table, ""))
	default:
		// Row security: enabling waits for the policies, which the diff
		// creates before it.
		p.short(change, statement(change.SQL, safety.AlterRLS, change.Table, ""))
	}
}

// renameTable renames first, in expand: every later change names the table
// by its new name. The @ratel.renamed intent behind it is the consent to a
// rename running code sees.
func (p *planner) renameTable(change *diff.Change) {
	from, target := change.Table, nameOf(change.Key)

	p.add(change, Expand, Short, statement(change.SQL, safety.RenameTable, from, ""))
	p.steps[len(p.steps)-1].Allow = append(p.steps[len(p.steps)-1].Allow, "RTL-C01")

	// The planner reads the current state under the new name; both names
	// exist on the target before the plan.
	p.env.Current = diff.Requalified(p.env.Current, from, target)

	relations := make(map[string]safety.Relation, len(p.env.Safety.Relations)+1)
	for name, relation := range p.env.Safety.Relations {
		relations[name] = relation
	}

	relations[target] = relations[from]
	p.env.Safety.Relations = relations
}

// unsafe records a change without a safe recipe; allowed, its statements
// are emitted as short DDL under the lock guards.
func (p *planner) unsafe(change *diff.Change, code, detail string, statements ...Statement) {
	p.find(change, code, detail, false)

	if len(statements) > 0 && p.allows(change, code) {
		p.short(change, statements...)
	}
}

func (p *planner) allows(change *diff.Change, code string) bool {
	for _, allowed := range p.allowed(change) {
		if allowed == code {
			return true
		}
	}

	return false
}

// nameOf is the object name of a rendering key.
func nameOf(key string) string {
	_, name, _ := strings.Cut(key, " ")

	return name
}

// checkName is the constraint name after a clause such as " ADD CONSTRAINT ".
func checkName(sql, clause string) string {
	_, rest, _ := strings.Cut(sql, clause)
	name, _, _ := strings.Cut(rest, " ")

	return name
}
