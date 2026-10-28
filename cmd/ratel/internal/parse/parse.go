// Package parse bounds access to the shared libpg_query WASM instance pool.
package parse

import (
	"fmt"

	pganalyze "github.com/pganalyze/pg_query_go/v6"
	pg "github.com/wasilibs/go-pgquery"
)

// DefaultConcurrency is the ratel.yaml parser.concurrency default.
const DefaultConcurrency = 1

// Config is the parser section of ratel.yaml. Zero selects the documented default.
type Config struct {
	Concurrency int `yaml:"concurrency"`
}

// ConfigError reports an invalid parser concurrency bound.
type ConfigError struct{ Concurrency int }

func (e *ConfigError) Error() string {
	return fmt.Sprintf("parser.concurrency must be positive, got %d", e.Concurrency)
}

// Parser shares one concurrency bound across parsing, scanning and deparsing.
// Construct once before concurrent use and inject copies into every consumer.
// The zero value is not usable; New validates configuration and warms WASM.
type Parser struct {
	slots chan struct{}
}

// New runs one parse so wazero caches its version before any concurrent
// use. wazero (v1.11.0, v1.12.0) fills internal/version's package global
// without synchronization on the first NewRuntime, and go-pgquery creates a
// runtime per pooled parser, so concurrent first parses race. Construct the
// application Parser before starting concurrent consumers.
func New(config Config) (Parser, error) {
	concurrency := config.Concurrency
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}

	if concurrency < 0 {
		return Parser{}, &ConfigError{Concurrency: concurrency}
	}

	parser := Parser{slots: make(chan struct{}, concurrency)}
	if _, err := parser.Parse(""); err != nil {
		return Parser{}, err
	}

	return parser, nil
}

// Parse parses one or more SQL statements into the libpg_query parse tree.
func (p Parser) Parse(sql string) (*pganalyze.ParseResult, error) {
	p.slots <- struct{}{}
	defer func() { <-p.slots }()

	result, err := pg.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("parse SQL: %w", err)
	}

	return result, nil
}

// Scan returns PostgreSQL tokens using the same bound as Parse and Deparse.
func (p Parser) Scan(sql string) (*pganalyze.ScanResult, error) {
	p.slots <- struct{}{}
	defer func() { <-p.slots }()

	result, err := pg.Scan(sql)
	if err != nil {
		return nil, fmt.Errorf("scan SQL: %w", err)
	}

	return result, nil
}

// Deparse renders a parse tree using the same bound as Parse and Scan.
func (p Parser) Deparse(tree *pganalyze.ParseResult) (string, error) {
	p.slots <- struct{}{}
	defer func() { <-p.slots }()

	result, err := pg.Deparse(tree)
	if err != nil {
		return "", fmt.Errorf("deparse SQL: %w", err)
	}

	return result, nil
}

// Stmt is one parsed statement with its 0-based ordinal.
type Stmt struct {
	Index int
	Node  *pganalyze.Node // the inner statement node (RawStmt.Stmt)
}

// Statements parses SQL and returns each top-level statement.
func (p Parser) Statements(sql string) ([]Stmt, error) {
	res, err := p.Parse(sql)
	if err != nil {
		return nil, err
	}

	out := make([]Stmt, 0, len(res.GetStmts()))
	for i, raw := range res.GetStmts() {
		out = append(out, Stmt{Index: i, Node: raw.GetStmt()})
	}

	return out, nil
}
