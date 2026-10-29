// Package plan rewrites diff changes into safe migration steps and assigns
// each to a phase. A step is the unit the runner applies: one statement that
// cannot run in a transaction, a short transaction of statements, or a
// batched backfill. The planner states the operations of every statement it
// writes, so each step is assessed by the same lock and effect table as a
// hand-written one.
package plan

import (
	"slices"

	"github.com/gopherex/ratel/pkg/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Statement is one SQL statement and what it does.
type Statement struct {
	SQL        string
	Operations []safety.Operation
}

// Fill is the batched UPDATE of a backfill step: Set is applied to the rows
// of Table where Where holds, in batches ordered by Key.
type Fill struct {
	Table string
	Key   []string
	Set   string
	Where string
}

// Step is a unit of a plan.
type Step struct {
	Phase      migrate.Phase
	Class      migrate.Class
	Tx         migrate.TxMode
	Statements []Statement
	Fill       *Fill // backfill steps only
	Safety     safety.Safety
	// Allow are the lint codes the schema object behind the step allows.
	Allow []string
	// Change is the key of the diff change the step comes from.
	Change string
}

// Finding is a lint code the planner raised for a change: Fixed when its
// recipe avoided the problem, otherwise the change has no safe recipe and
// the plan fails unless the object allows the code.
type Finding struct {
	Code   string
	Change string
	Fixed  bool
	Detail string
}

// Plan is the steps of a migration in run order and the planner's findings.
type Plan struct {
	Steps    []Step
	Findings []Finding
}

// Intents are the schema annotations the planner uses.
type Intents struct {
	// Allow maps an object's rendering key, or a change key, to the lint
	// codes @ratel.allow permits on it.
	Allow map[string][]string
	// Backfill maps "column s.t.c" to the expression existing rows get.
	Backfill map[string]string
	// Retype maps "column s.t.c" to the USING expression of its type change.
	Retype map[string]string
}

// Env is what the planner knows of the target.
type Env struct {
	Safety  safety.Env
	Current introspect.Rendering
	Desired introspect.Rendering
	// Rewrites are the changes, by key, that the dev server probe found to
	// rewrite their table; Reindexes those found to rebuild its indexes.
	Rewrites  map[string]bool
	Reindexes map[string]bool
	// Dependents maps a view, routine or type key to the rendering keys of
	// the objects using it: views, routines, policies and triggers, which a
	// replacement recreates, and "column s.t.c", "default s.t.c" and
	// "constraint s.t.c" users, which it cannot.
	Dependents map[string][]string
}

// Build plans the changes in order. Each change becomes the steps of its
// recipe; steps are ordered by phase, keeping change order within a phase.
func Build(changes []diff.Change, env *Env, intents Intents) Plan {
	planner := newPlanner(env, intents)

	for index := 0; index < len(changes); index++ {
		change := &changes[index]

		// The diff replaces an object it cannot alter by its drop followed
		// by its create.
		if index+1 < len(changes) && changes[index+1].Key == change.Key && change.Before != "" &&
			change.After == "" && changes[index+1].Before == "" && changes[index+1].After != "" {
			planner.replace(change, &changes[index+1])

			index++

			continue
		}

		planner.change(change)
	}

	return planner.plan()
}

// planner collects the steps and findings of one plan.
type planner struct {
	env      Env
	intents  Intents
	steps    []Step
	findings []Finding
	// late are contract steps that must follow every other one: dropping
	// the old column of a rename after the objects using it are replaced.
	late []Step
	// notNulled are the "s.t.c" columns the plan already makes NOT NULL.
	notNulled map[string]bool
	// rebuilt are the column change keys a swap or rename recreates from
	// the desired definition, so their own changes are already done.
	rebuilt map[string]bool
	// floor is the earliest phase of each table's changes: a partition
	// detached in contract changes after its detach.
	floor map[string]migrate.Phase
}

func newPlanner(env *Env, intents Intents) *planner {
	return &planner{
		env: *env, intents: intents,
		notNulled: make(map[string]bool), rebuilt: make(map[string]bool), floor: make(map[string]migrate.Phase),
	}
}

// replace plans an object dropped and created again: an index or constraint
// is built under a temporary name beside the old one, anything else is
// dropped and created in one short transaction.
func (p *planner) replace(drop, create *diff.Change) {
	switch {
	case drop.Kind == diff.DropIndex:
		p.replaceIndex(drop, create)
	case drop.Kind == diff.DropConstraint && create.Kind != diff.AddPrimaryKey && p.env.Safety.Exists(create.Table):
		name := constraintName(create.Key)
		p.addConstraint(create, generated(name, "_ratel_new"), name)
	case drop.Kind == diff.DropConstraint:
		p.change(drop)
		p.change(create)
	default:
		users, recreatable := p.users(drop.Key)
		if !recreatable {
			p.unsafe(drop, "RTL-D01", drop.Key+" is used by a column, default or constraint that would be lost")

			return
		}

		planned := newPlanner(&p.env, p.intents)
		for index := len(users) - 1; index >= 0; index-- {
			planned.change(&users[index].drop)
		}

		planned.change(drop)
		planned.change(create)

		for index := range users {
			planned.change(&users[index].create)
		}

		p.findings = append(p.findings, planned.findings...)

		var statements []Statement

		p.late = append(p.late, planned.late...)

		for index := range planned.steps {
			if planned.steps[index].Class != migrate.ClassShort {
				// A step that cannot share a transaction keeps its own.
				p.steps = append(p.steps, planned.steps...)

				return
			}

			statements = append(statements, planned.steps[index].Statements...)
		}

		p.add(drop, p.phaseOf(statements), migrate.ClassShort, statements...)
	}
}

func (p *planner) plan() Plan {
	p.steps = append(p.steps, p.late...)

	phases := []migrate.Phase{migrate.PhaseExpand, migrate.PhaseBackfill, migrate.PhaseContract}

	slices.SortStableFunc(p.steps, func(a, b Step) int {
		return slices.Index(phases, a.Phase) - slices.Index(phases, b.Phase)
	})

	created := make(map[string]bool)

	for index := range p.steps {
		step := &p.steps[index]
		env := p.env.Safety
		env.Created = created

		var operations []safety.Operation
		for _, statement := range step.Statements {
			operations = append(operations, statement.Operations...)
		}

		step.Safety = safety.Assess(operations, env)

		for _, operation := range operations {
			if creates(operation.Kind) && !p.env.Safety.Exists(operation.Relation) {
				created[operation.Relation] = true
			}
		}
	}

	return Plan{Steps: p.steps, Findings: p.findings}
}

func creates(kind safety.Kind) bool {
	return kind == safety.CreateTable || kind == safety.CreateView || kind == safety.CreateSequence
}

// add appends a step for a change, allowing what the change's object allows.
func (p *planner) add(change *diff.Change, phase migrate.Phase, class migrate.Class, statements ...Statement) {
	mode := migrate.InTx
	if class == migrate.ClassLong {
		mode = migrate.OwnStatement
	}

	if p.floor[change.Table] == migrate.PhaseContract {
		phase = migrate.PhaseContract
	}

	p.steps = append(p.steps, Step{
		Phase: phase, Class: class, Tx: mode, Statements: statements, Allow: p.allowed(change), Change: change.Key,
	})
}

// short adds a short DDL step in the phase the change's compatibility puts
// it in.
func (p *planner) short(change *diff.Change, statements ...Statement) {
	p.add(change, p.phaseOf(statements), migrate.ClassShort, statements...)
}

// phaseOf places statements by what running code sees: breaking and
// destroying ones go to contract, the rest to expand.
func (p *planner) phaseOf(statements []Statement) migrate.Phase {
	var operations []safety.Operation
	for _, statement := range statements {
		operations = append(operations, statement.Operations...)
	}

	assessed := safety.Assess(operations, p.env.Safety)
	if assessed.Compat == safety.Breaking || assessed.Destroys != safety.None {
		return migrate.PhaseContract
	}

	return migrate.PhaseExpand
}

// find records a finding; an unfixed one fails the plan unless allowed.
func (p *planner) find(change *diff.Change, code, detail string, fixed bool) {
	p.findings = append(p.findings, Finding{Code: code, Change: change.Key, Fixed: fixed, Detail: detail})
}

// allowed is what @ratel.allow permits on the change's object and table.
func (p *planner) allowed(change *diff.Change) []string {
	var codes []string

	for _, key := range []string{change.Key, "table " + change.Table} {
		codes = append(codes, p.intents.Allow[key]...)
	}

	slices.Sort(codes)

	return slices.Compact(codes)
}

// statement is one SQL statement doing one operation.
func statement(sql string, kind safety.Kind, relation, other string) Statement {
	return Statement{SQL: sql, Operations: []safety.Operation{{Kind: kind, Relation: relation, Other: other}}}
}
