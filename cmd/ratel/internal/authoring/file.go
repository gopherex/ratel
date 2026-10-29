package authoring

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/pkg/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Step is a migration step and what its statements do.
type Step struct {
	migrate.Step
	// Operations are what the statements do: as the planner stated them in
	// the header, or as Classify found them in hand-written SQL.
	Operations []safety.Operation
	// PerStatement are the operations of each statement of a classified
	// hand-written step.
	PerStatement [][]safety.Operation
}

// File is a migration file, its text and its steps in run order.
type File struct {
	Name  string
	SQL   string
	Steps []Step
}

// Parse reads a migration file; a planned step's operations come from its
// header, a hand-written step's from Classify.
func Parse(source migrate.Source) (File, error) {
	parsed, err := migrate.Parse(source.Name, source.SQL)
	if err != nil {
		return File{}, err
	}

	file := File{Name: source.Name, SQL: source.SQL, Steps: make([]Step, 0, len(parsed.Steps))}

	for index := range parsed.Steps {
		step := &parsed.Steps[index]

		operations, err := DecodeOps(step.Ops)
		if err != nil {
			return File{}, fmt.Errorf("%s step %s: %w", source.Name, step.ID, err)
		}

		file.Steps = append(file.Steps, Step{Step: *step, Operations: operations})
	}

	return file, nil
}

// Seal writes the header of every hand-written step: its transaction mode,
// class, locks and operations as Classify found them, marked by=hand, so
// the runner applies it without a parser. major is the PostgreSQL version
// the file is planned for.
func Seal(file *File, major int) string {
	steps := make([]migrate.Step, 0, len(file.Steps))

	for index := range file.Steps {
		step := &file.Steps[index]
		if !step.Planned() {
			// The id follows the statements, which may have been edited since
			// the step was last sealed.
			step.ID = migrate.StepID(file.Name, step.Statements)
			step.Sealed, step.Hand, step.Major = true, true, major
			step.Ops = EncodeOps(step.Operations)
		}

		steps = append(steps, step.Step)
	}

	return migrate.Seal(file.SQL, migrate.File{Name: file.Name, Steps: steps})
}

// Write renders a plan as the planned file name, one block per step.
func Write(name string, planned plan.Plan, major int) []byte {
	var out strings.Builder

	for index := range planned.Steps {
		step := &planned.Steps[index]
		statements := make([]string, 0, len(step.Statements))

		var operations []safety.Operation

		for _, statement := range step.Statements {
			statements = append(statements, strings.TrimSuffix(strings.TrimSpace(statement.SQL), ";"))
			operations = append(operations, statement.Operations...)
		}

		file := migrate.Step{
			ID: migrate.StepID(name, statements), Phase: step.Phase, Class: step.Class, Tx: step.Safety.Tx,
			Locks: step.Safety.Locks, Major: major, Allow: step.Allow, Ops: EncodeOps(operations),
			Statements: statements, Sealed: true,
		}
		if step.Fill != nil {
			file.Key = step.Fill.Key
		}

		if index > 0 {
			out.WriteString("\n")
		}

		out.WriteString(file.Header() + "\n")

		for _, statement := range statements {
			out.WriteString(statement + ";\n")
		}
	}

	return []byte(out.String())
}

// EncodeOps is the ops= field of the operations.
func EncodeOps(operations []safety.Operation) string {
	encoded := make([]string, 0, len(operations))
	for _, operation := range operations {
		encoded = append(encoded, string(operation.Kind)+":"+migrate.Escape(operation.Relation)+":"+
			migrate.Escape(operation.Other)+":"+strconv.FormatBool(operation.Rewrite))
	}

	return strings.Join(encoded, ",")
}

// operationFields are kind, relation, other and rewrite.
const operationFields = 4

// DecodeOps reads an ops= field; an empty one is no operations.
func DecodeOps(value string) ([]safety.Operation, error) {
	if value == "" {
		return nil, nil
	}

	var operations []safety.Operation

	for item := range strings.SplitSeq(value, ",") {
		parts := strings.Split(item, ":")
		if len(parts) != operationFields {
			return nil, fmt.Errorf("%w: operation %q", migrate.ErrHeader, item)
		}

		relation, err := migrate.Unescape(parts[1])
		if err != nil {
			return nil, err
		}

		other, err := migrate.Unescape(parts[2])
		if err != nil {
			return nil, err
		}

		operations = append(operations, safety.Operation{
			Kind: safety.Kind(parts[0]), Relation: relation, Other: other, Rewrite: parts[3] == "true",
		})
	}

	return operations, nil
}
