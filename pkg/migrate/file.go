// Package migrate applies the migrations ratel plans to a PostgreSQL
// database without locking it: an application embeds its migrations
// directory and calls Apply on startup, as `ratel migrate apply` does.
//
// A run verifies ratel.sum, takes an advisory lock, records every step in
// the ratel.migrations_journal table, waits for conflicting sessions before
// taking a lock, retries lock timeouts on the GitLab schedule, resumes
// interrupted index builds and backfills, and fills rows in key-ordered
// batches. The format of the files and every guarantee are specified in
// docs/migrations.md.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
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
	Phase Phase
	Class Class
	Tx    TxMode
	Locks []RelLock
	Major int // pg=; zero when a hand-written step states none
	Allow []string
	// Ops is the ops= field as written: what the statements do, for the
	// ratel CLI's lint; the runner does not read it.
	Ops        string
	Key        []string // backfill key columns
	Statements []string
	// Sealed marks a step whose header carries an id, and with it the
	// transaction mode, class and locks the runner needs.
	Sealed bool
	// Hand marks a hand-written step sealed by ratel migrate hash (by=hand).
	Hand bool
	// at is the offset of the step's header line, or of its first statement
	// when it has no header; headed tells which.
	at     int
	headed bool
}

// Planned reports a step the planner wrote.
func (s *Step) Planned() bool { return s.Sealed && !s.Hand }

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

// Header is the step's header line.
func (s *Step) Header() string {
	tx := "in"
	if s.Tx == OwnStatement {
		tx = "own"
	}

	fields := []string{header, "id=" + s.ID, "phase=" + string(s.Phase), "tx=" + tx, "class=" + string(s.Class)}

	if s.Hand {
		fields = append(fields, "by=hand")
	}

	if len(s.Locks) > 0 {
		locks := make([]string, 0, len(s.Locks))
		for _, lock := range s.Locks {
			locks = append(locks, Escape(lock.Relation)+":"+lock.Mode.String())
		}

		fields = append(fields, "locks="+strings.Join(locks, ","))
	}

	fields = append(fields, "pg="+strconv.Itoa(s.Major))

	if len(s.Allow) > 0 {
		fields = append(fields, "allow="+strings.Join(s.Allow, ","))
	}

	if s.Ops != "" {
		fields = append(fields, "ops="+s.Ops)
	}

	if len(s.Key) > 0 {
		fields = append(fields, "key="+Escape(strings.Join(s.Key, ",")))
	}

	return strings.Join(fields, " ")
}

// Escape keeps a name free of the spaces, colons and commas that separate
// header fields.
func Escape(value string) string { return url.QueryEscape(value) }

// Unescape reverses Escape.
func Unescape(value string) (string, error) {
	unescaped, err := url.QueryUnescape(value)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHeader, err)
	}

	return unescaped, nil
}

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

			step.at, step.headed = piece.Start, true
			file.Steps = append(file.Steps, step)
			current = &file.Steps[len(file.Steps)-1]
		case piece.Comment && strings.HasPrefix(piece.Text, allowComment):
			code, _, _ := strings.Cut(strings.TrimPrefix(piece.Text, allowComment), " ")
			allow = append(allow, code)
		case piece.Comment:
		case current == nil:
			// A hand-written statement outside a block is its own step.
			file.Steps = append(file.Steps, Step{
				Phase: PhaseExpand, Statements: []string{piece.Text}, Allow: allow, at: piece.Start,
			})
			allow = nil
		default:
			current.Statements = append(current.Statements, piece.Text)
			current.Allow = append(current.Allow, allow...)
			allow = nil
		}
	}

	for index := range file.Steps {
		step := &file.Steps[index]
		if step.ID == "" {
			step.ID = StepID(name, step.Statements)
		}

		// A sealed hand-written step repeats its allow comments in its header.
		slices.Sort(step.Allow)
		step.Allow = slices.Compact(step.Allow)
	}

	return file, nil
}

// Seal rewrites the text of a parsed file with the header of every step:
// a step's header line is replaced, or written before its first statement.
// Statements, comments and allow comments stay as they are.
func Seal(sql string, file File) string {
	var out strings.Builder

	last := 0

	for index := range file.Steps {
		step := &file.Steps[index]
		out.WriteString(sql[last:step.at])
		out.WriteString(step.Header() + "\n")

		last = step.at
		if step.headed {
			last = lineEnd(sql, step.at)
		}
	}

	out.WriteString(sql[last:])

	return out.String()
}

// parseHeader reads the fields of a step header; only a header with an id
// is sealed.
func parseHeader(text string) (Step, error) {
	step := Step{Phase: PhaseExpand}

	for _, field := range strings.Fields(strings.TrimPrefix(text, header)) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return Step{}, fmt.Errorf("%w: field %q has no value", ErrHeader, field)
		}

		if err := step.set(key, value); err != nil {
			return Step{}, err
		}
	}

	step.Sealed = step.ID != ""

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
		s.Class, err = class(value)
	case "by":
		s.Hand, err = value == "hand", by(value)
	case "locks":
		s.Locks, err = parseLocks(value)
	case "pg":
		s.Major, err = strconv.Atoi(value)
	case "allow":
		s.Allow = strings.Split(value, ",")
	case "ops":
		s.Ops = value
	case "key":
		var columns string
		if columns, err = Unescape(value); err == nil {
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

func phase(value string) (Phase, error) {
	switch parsed := Phase(value); parsed {
	case PhaseExpand, PhaseBackfill, PhaseContract:
		return parsed, nil
	default:
		return "", fmt.Errorf("%w: unknown phase %q", ErrHeader, value)
	}
}

func class(value string) (Class, error) {
	switch parsed := Class(value); parsed {
	case ClassShort, ClassLong, ClassBatched:
		return parsed, nil
	default:
		return "", fmt.Errorf("%w: unknown class %q", ErrHeader, value)
	}
}

func by(value string) error {
	if value != "hand" {
		return fmt.Errorf("%w: unknown author %q", ErrHeader, value)
	}

	return nil
}

func txMode(value string) (TxMode, error) {
	switch value {
	case "in":
		return InTx, nil
	case "own":
		return OwnStatement, nil
	default:
		return InTx, fmt.Errorf("%w: unknown mode %q", ErrHeader, value)
	}
}

func parseLocks(value string) ([]RelLock, error) {
	var locks []RelLock

	for _, item := range strings.Split(value, ",") {
		relation, mode, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("%w: lock %q has no mode", ErrHeader, item)
		}

		name, err := Unescape(relation)
		if err != nil {
			return nil, err
		}

		parsed := lockMode(mode)
		if parsed == NoLock {
			return nil, fmt.Errorf("%w: unknown lock mode %q", ErrHeader, mode)
		}

		locks = append(locks, RelLock{Relation: name, Mode: parsed})
	}

	return locks, nil
}

func lockMode(name string) LockMode {
	for mode := AccessShare; mode <= AccessExclusive; mode++ {
		if mode.String() == name {
			return mode
		}
	}

	return NoLock
}
