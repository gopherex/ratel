// Package ident renders identifiers as PostgreSQL's quote_ident and format's
// %I do, so names built by ratel match the names introspection renders.
package ident

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// Quoter quotes identifiers, asking the parser which words are keywords.
// It caches answers and is not safe for concurrent use.
type Quoter struct {
	parser parse.Parser
	quoted map[string]string
}

// New is a quoter over parser.
func New(parser parse.Parser) *Quoter {
	return &Quoter{parser: parser, quoted: make(map[string]string)}
}

// Quote quotes an identifier unless it is lower case, simple and not a
// keyword PostgreSQL reserves in any position.
func (q *Quoter) Quote(name string) string {
	if quoted, ok := q.quoted[name]; ok {
		return quoted
	}

	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if simple(name) && !q.keyword(name) {
		quoted = name
	}

	q.quoted[name] = quoted

	return quoted
}

// Qualified quotes each part and joins them with dots.
func (q *Quoter) Qualified(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, q.Quote(part))
	}

	return strings.Join(quoted, ".")
}

func (q *Quoter) keyword(name string) bool {
	scan, err := q.parser.Scan(name)
	if err != nil || len(scan.GetTokens()) != 1 {
		return true
	}

	kind := scan.GetTokens()[0].GetKeywordKind()

	return kind != pg.KeywordKind_NO_KEYWORD && kind != pg.KeywordKind_UNRESERVED_KEYWORD
}

func simple(name string) bool {
	for index, char := range name {
		letter := char >= 'a' && char <= 'z' || char == '_'
		suffix := index > 0 && (char >= '0' && char <= '9' || char == '$')

		if !letter && !suffix {
			return false
		}
	}

	return name != ""
}
