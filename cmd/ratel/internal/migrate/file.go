// Package migrate writes, reads, checksums and applies migration files.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// header starts a step block.
const header = "-- ratel:step"

// allowComment starts the comment allowing a lint code on the next
// statement of a hand-written file.
const allowComment = "-- @ratel.allow "

// idLength is the hex digits of a step id.
const idLength = 8

// Step is one step of a migration file.
type Step struct {
	ID    string
	Phase plan.Phase
	Class plan.Class
	Tx    safety.TxMode
	Locks []safety.RelLock
	Major int // pg=; zero when a hand-written step states none
	Allow []string
	// Operations are what a planned step's statements do, as the planner
	// stated them; nil for a hand-written step, which is classified.
	Operations []safety.Operation
	// PerStatement are the operations of each statement of a classified
	// hand-written step.
	PerStatement [][]safety.Operation
	Key          []string // backfill key columns
	Statements   []string
	// Planned marks a step written by the planner, with its header complete.
	Planned bool
}

// File is a migration file and its steps in run order.
type File struct {
	Name  string
	Steps []Step
}

// FileName is the name of a planned file: UTC timestamp, then the name.
func FileName(at time.Time, name string) string {
	return at.UTC().Format("20060102150405") + "_" + name + ".sql"
}

// StepID is the hash of the file name and the step's normalized SQL.
func StepID(file string, statements []string) string {
	sum := sha256.Sum256([]byte(file + "\n" + strings.Join(statements, ";\n")))

	return hex.EncodeToString(sum[:])[:idLength]
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

		file := Step{
			ID: StepID(name, statements), Phase: step.Phase, Class: step.Class, Tx: step.Safety.Tx,
			Locks: step.Safety.Locks, Major: major, Allow: step.Allow, Operations: operations, Statements: statements,
		}
		if step.Fill != nil {
			file.Key = step.Fill.Key
		}

		if index > 0 {
			out.WriteString("\n")
		}

		out.WriteString(file.header() + "\n")

		for _, statement := range statements {
			out.WriteString(statement + ";\n")
		}
	}

	return []byte(out.String())
}

func (s *Step) header() string {
	tx := "in"
	if s.Tx == safety.OwnStatement {
		tx = "own"
	}

	fields := []string{header, "id=" + s.ID, "phase=" + string(s.Phase), "tx=" + tx, "class=" + string(s.Class)}

	if len(s.Locks) > 0 {
		locks := make([]string, 0, len(s.Locks))
		for _, lock := range s.Locks {
			locks = append(locks, escape(lock.Relation)+":"+lock.Mode.String())
		}

		fields = append(fields, "locks="+strings.Join(locks, ","))
	}

	fields = append(fields, "pg="+strconv.Itoa(s.Major))

	if len(s.Allow) > 0 {
		fields = append(fields, "allow="+strings.Join(s.Allow, ","))
	}

	if len(s.Operations) > 0 {
		operations := make([]string, 0, len(s.Operations))
		for _, operation := range s.Operations {
			operations = append(operations, string(operation.Kind)+":"+escape(operation.Relation)+":"+
				escape(operation.Other)+":"+strconv.FormatBool(operation.Rewrite))
		}

		fields = append(fields, "ops="+strings.Join(operations, ","))
	}

	if len(s.Key) > 0 {
		fields = append(fields, "key="+escape(strings.Join(s.Key, ",")))
	}

	return strings.Join(fields, " ")
}

// escape keeps a name free of the spaces, colons and commas that separate
// header fields.
func escape(value string) string { return url.QueryEscape(value) }

// Parse reads a migration file: planned blocks, hand-written statements
// under optional headers, and allow comments before statements.
func Parse(name, sql string) (File, error) {
	file := File{Name: name}

	var (
		current *Step
		allow   []string
	)

	for _, piece := range split(sql) {
		switch {
		case piece.Comment && strings.HasPrefix(piece.Text, header):
			step, err := parseHeader(piece.Text)
			if err != nil {
				return File{}, fmt.Errorf("%s: %w", name, err)
			}

			file.Steps = append(file.Steps, step)
			current = &file.Steps[len(file.Steps)-1]
		case piece.Comment && strings.HasPrefix(piece.Text, allowComment):
			code, _, _ := strings.Cut(strings.TrimPrefix(piece.Text, allowComment), " ")
			allow = append(allow, code)
		case piece.Comment:
		case current == nil:
			// A hand-written statement outside a block is its own step.
			file.Steps = append(file.Steps, Step{Phase: plan.Expand, Statements: []string{piece.Text}, Allow: allow})
			allow = nil
		default:
			current.Statements = append(current.Statements, piece.Text)
			current.Allow = append(current.Allow, allow...)
			allow = nil
		}
	}

	for index := range file.Steps {
		if file.Steps[index].ID == "" {
			file.Steps[index].ID = StepID(name, file.Steps[index].Statements)
		}
	}

	return file, nil
}

// parseHeader reads the fields of a step header; only a header with an id
// is a complete planned one.
func parseHeader(text string) (Step, error) {
	step := Step{Phase: plan.Expand}

	for _, field := range strings.Fields(strings.TrimPrefix(text, header)) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return Step{}, fmt.Errorf("%w: field %q has no value", ErrHeader, field)
		}

		if err := step.set(key, value); err != nil {
			return Step{}, err
		}
	}

	step.Planned = step.ID != ""

	return step, nil
}

// ErrHeader is a step header ratel cannot read.
var ErrHeader = errors.New("invalid step header")

func (s *Step) set(field, value string) error {
	var err error

	switch field {
	case "id":
		s.ID = value
	case "phase":
		s.Phase, err = phase(value)
	case "tx":
		s.Tx, err = txMode(value)
	case "class":
		s.Class = plan.Class(value)
	case "locks":
		s.Locks, err = parseLocks(value)
	case "pg":
		s.Major, err = strconv.Atoi(value)
	case "allow":
		s.Allow = strings.Split(value, ",")
	case "ops":
		s.Operations, err = parseOperations(value)
	case "key":
		var columns string
		if columns, err = unescape(value); err == nil {
			s.Key = strings.Split(columns, ",")
		}
	default:
		return fmt.Errorf("%w: unknown field %q", ErrHeader, field)
	}

	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrHeader, field, err)
	}

	return nil
}

func phase(value string) (plan.Phase, error) {
	switch parsed := plan.Phase(value); parsed {
	case plan.Expand, plan.Backfill, plan.Contract:
		return parsed, nil
	default:
		return "", fmt.Errorf("%w: unknown phase %q", ErrHeader, value)
	}
}

func txMode(value string) (safety.TxMode, error) {
	switch value {
	case "in":
		return safety.InTx, nil
	case "own":
		return safety.OwnStatement, nil
	default:
		return safety.InTx, fmt.Errorf("%w: unknown mode %q", ErrHeader, value)
	}
}

func unescape(value string) (string, error) {
	unescaped, err := url.QueryUnescape(value)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHeader, err)
	}

	return unescaped, nil
}

func parseLocks(value string) ([]safety.RelLock, error) {
	var locks []safety.RelLock

	for _, item := range strings.Split(value, ",") {
		relation, mode, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("%w: lock %q has no mode", ErrHeader, item)
		}

		name, err := unescape(relation)
		if err != nil {
			return nil, err
		}

		parsed := lockMode(mode)
		if parsed == safety.NoLock {
			return nil, fmt.Errorf("%w: unknown lock mode %q", ErrHeader, mode)
		}

		locks = append(locks, safety.RelLock{Relation: name, Mode: parsed})
	}

	return locks, nil
}

func lockMode(name string) safety.LockMode {
	for mode := safety.AccessShare; mode <= safety.AccessExclusive; mode++ {
		if mode.String() == name {
			return mode
		}
	}

	return safety.NoLock
}

// operationFields are kind, relation, other and rewrite.
const operationFields = 4

func parseOperations(value string) ([]safety.Operation, error) {
	var operations []safety.Operation

	for _, item := range strings.Split(value, ",") {
		parts := strings.Split(item, ":")
		if len(parts) != operationFields {
			return nil, fmt.Errorf("%w: operation %q", ErrHeader, item)
		}

		relation, err := unescape(parts[1])
		if err != nil {
			return nil, err
		}

		other, err := unescape(parts[2])
		if err != nil {
			return nil, err
		}

		operations = append(operations, safety.Operation{
			Kind: safety.Kind(parts[0]), Relation: relation, Other: other, Rewrite: parts[3] == "true",
		})
	}

	return operations, nil
}
