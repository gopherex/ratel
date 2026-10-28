package parse

import (
	"fmt"

	pganalyze "github.com/pganalyze/pg_query_go/v6"
	pg "github.com/wasilibs/go-pgquery"
)

// Parse parses one or more SQL statements into the libpg_query parse tree.
func Parse(sql string) (*pganalyze.ParseResult, error) {
	result, err := pg.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("parse SQL: %w", err)
	}

	return result, nil
}

// Warm runs one parse so wazero caches its version before any concurrent
// use. wazero (v1.11.0, v1.12.0) fills internal/version's package global
// without synchronization on the first NewRuntime, and go-pgquery creates a
// runtime per pooled parser, so concurrent first parses race. Callers that
// parse concurrently call Warm once before starting.
func Warm() {
	_, _ = pg.Parse("")
}

// Stmt is one parsed statement with its 0-based ordinal.
type Stmt struct {
	Index int
	Node  *pganalyze.Node // the inner statement node (RawStmt.Stmt)
}

// Statements parses SQL and returns each top-level statement.
func Statements(sql string) ([]Stmt, error) {
	res, err := Parse(sql)
	if err != nil {
		return nil, err
	}

	out := make([]Stmt, 0, len(res.GetStmts()))
	for i, raw := range res.GetStmts() {
		out = append(out, Stmt{Index: i, Node: raw.GetStmt()})
	}

	return out, nil
}
