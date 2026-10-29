package lint

import (
	"slices"
	"strings"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// sameTransactionEnum is the first major version where a value added to an
// enum created in the same transaction is usable before commit.
const sameTransactionEnum = 17

// Steps lints the classified steps of one file against the state before it:
// env holds the existing relations, policies the tables that have policies.
// A planned step is checked for its structure and for breaking changes in
// expand, the planner having answered for its recipe; a hand-written step
// also for every code its statements carry.
func Steps(file *authoring.File, env safety.Env, policies map[string]bool) []Finding {
	var findings []Finding

	env.Created = make(map[string]bool)
	policies = maps(policies)

	for index := range file.Steps {
		step := &file.Steps[index]
		assessed := safety.Assess(step.Operations, env)
		where := file.Name + " " + step.ID

		var codes []string

		for _, code := range assessed.Reasons {
			if !step.Planned() || step.Phase == migrate.PhaseExpand && assessed.Compat == safety.Breaking &&
				strings.HasPrefix(code, "RTL-C") {
				codes = append(codes, code)
			}
		}

		codes = append(codes, structure(step, assessed, env)...)

		if !step.Planned() {
			codes = append(codes, handWritten(step, env, policies)...)
		}

		slices.Sort(codes)

		for _, code := range slices.Compact(codes) {
			findings = append(findings, Finding{
				Code: code, Where: where, Detail: detail(code), Allowed: slices.Contains(step.Allow, code),
			})
		}

		track(step, env, policies)
	}

	return findings
}

func maps(policies map[string]bool) map[string]bool {
	result := make(map[string]bool, len(policies))
	for table := range policies {
		result[table] = true
	}

	return result
}

// track records what a step creates for the steps after it.
func track(step *authoring.Step, env safety.Env, policies map[string]bool) {
	for _, operation := range step.Operations {
		switch operation.Kind {
		case safety.CreateTable, safety.CreateView, safety.CreateSequence:
			if !env.Exists(operation.Relation) {
				env.Created[operation.Relation] = true
			}
		case safety.CreatePolicy:
			policies[operation.Relation] = true
		default:
		}
	}
}

// structure checks how a step's statements share a transaction.
func structure(step *authoring.Step, assessed safety.Safety, env safety.Env) []string {
	var codes []string

	if assessed.Tx == migrate.OwnStatement && len(step.Statements) > 1 {
		codes = append(codes, "RTL-T01")
	}

	if has(step.Operations, safety.AddEnumValue) && len(step.Statements) > 1 &&
		(env.Major < sameTransactionEnum || !has(step.Operations, safety.CreateType)) {
		codes = append(codes, "RTL-T02")
	}

	blocking := 0

	for _, lock := range assessed.Locks {
		if lock.Mode.Blocks() {
			blocking++
		}
	}

	if blocking > 1 {
		codes = append(codes, "RTL-T05")
	}

	if step.Class == migrate.ClassLong && blocking > 0 {
		codes = append(codes, "RTL-L01")
	}

	return codes
}

// setsTimeout reports a statement setting a timeout the runner guards with.
func setsTimeout(statement string) bool {
	fields := strings.Fields(strings.ToLower(statement))
	if len(fields) < 2 || fields[0] != "set" {
		return false
	}

	name := fields[1]
	if (name == "local" || name == "session") && len(fields) > 2 {
		name = fields[2]
	}

	return name == "lock_timeout" || name == "statement_timeout"
}

// handWritten checks what only hand-written SQL can get wrong: slow work
// after an exclusive lock, timeouts overriding the guards, and row security
// enabled before any policy.
func handWritten(step *authoring.Step, env safety.Env, policies map[string]bool) []string {
	var codes []string

	locked := false

	for index, operations := range step.PerStatement {
		single := safety.Assess(operations, env)
		if locked && single.Effect >= safety.Scan {
			codes = append(codes, "RTL-T03")
		}

		for _, lock := range single.Locks {
			locked = locked || lock.Mode.Blocks()
		}

		if setsTimeout(step.Statements[index]) {
			codes = append(codes, "RTL-T04")
		}

		for _, operation := range operations {
			if operation.Kind == safety.AlterRLS && !policies[operation.Relation] &&
				strings.Contains(strings.ToUpper(step.Statements[index]), "ENABLE ROW LEVEL SECURITY") {
				codes = append(codes, "RTL-C08")
			}
		}
	}

	return codes
}

func has(operations []safety.Operation, kind safety.Kind) bool {
	return slices.ContainsFunc(operations, func(operation safety.Operation) bool { return operation.Kind == kind })
}
