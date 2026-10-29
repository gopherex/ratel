package annotate

import (
	"cmp"
	"slices"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

type header struct {
	name       string
	command    ir.QueryCommand
	start, end int
}

// QuerySource is one named statement before parameter rewriting and inference.
// SQL is the exact original token range; Meta.Source identifies that range.
type QuerySource struct {
	Name, SQL string
	Command   ir.QueryCommand
	Header    *ir.SourceSpan
	Meta      *ir.Metadata
}

// QuerySet is the source query list and nonfatal namespace diagnostics.
type QuerySet struct {
	Queries     []*QuerySource
	Diagnostics []*ir.Diagnostic
}

// Queries splits named-query files before parameter rewriting and inference.
// SQL and spans retain original source bytes. Names are unique across files.
func Queries(parser parse.Parser, files []File, owned []string) (*QuerySet, error) {
	ordered := slices.Clone(files)
	slices.SortFunc(ordered, func(left, right File) int { return cmp.Compare(left.Name, right.Name) })

	seen := make(map[string]*ir.SourceSpan)
	result := &QuerySet{}

	for _, file := range ordered {
		queries, err := queryFile(parser, file)
		if err != nil {
			return nil, err
		}

		for _, query := range queries {
			if previous := seen[query.Name]; previous != nil {
				message := "duplicate query name; first declared at " + sourcePosition(previous)

				return nil, invalid(query.Header, message)
			}

			seen[query.Name] = query.Header
			result.Queries = append(result.Queries, query)
			result.Diagnostics = append(result.Diagnostics, queryWarnings(query, owned)...)
		}
	}

	return result, nil
}

func queryWarnings(query *QuerySource, owned []string) []*ir.Diagnostic {
	var result []*ir.Diagnostic

	for _, value := range query.Meta.GetAnnotations() {
		if warning := unowned(value, owned); warning != nil {
			result = append(result, warning)
		}
	}

	return result
}

func queryFile(parser parse.Parser, file File) ([]*QuerySource, error) {
	tokens, err := scanSQL(parser, file.Name, file.SQL)
	if err != nil {
		return nil, err
	}

	headers, err := queryHeaders(file, tokens)
	if err != nil {
		return nil, err
	}

	entries, err := scanAnnotations(file.Name, file.SQL, tokens)
	if err != nil {
		return nil, err
	}

	var result []*QuerySource

	for index, item := range headers {
		end := len(file.SQL)
		if index+1 < len(headers) {
			end = headers[index+1].start
		}

		query, err := splitQuery(parser, file, item, end, tokens, entries)
		if err != nil {
			return nil, err
		}

		result = append(result, query)
	}

	for _, entry := range entries {
		if len(headers) == 0 || entry.Start < headers[0].start {
			return nil, invalid(entry.Value.GetSource(), "annotation has no query target")
		}
	}

	return result, nil
}

func queryHeaders(file File, tokens []*pg.ScanToken) ([]header, error) {
	var result []header

	for _, token := range tokens {
		if !isComment(token) {
			if len(result) == 0 {
				source := span(file.Name, file.SQL, int(token.GetStart()), int(token.GetEnd()))

				return nil, invalid(source, "SQL requires a query header")
			}

			continue
		}

		if token.GetToken() != pg.Token_SQL_COMMENT {
			continue
		}

		start, end := int(token.GetStart()), int(token.GetEnd())
		lineStart := strings.LastIndexByte(file.SQL[:start], '\n') + 1

		if strings.TrimSpace(file.SQL[lineStart:start]) != "" {
			continue
		}

		text := strings.TrimSpace(file.SQL[start+len("--") : end])
		if !strings.HasPrefix(text, "name:") {
			continue
		}

		source := span(file.Name, file.SQL, start, end)
		fields := strings.Fields(strings.TrimPrefix(text, "name:"))

		const headerFields = 2

		if len(fields) != headerFields || !exported(fields[0]) {
			return nil, invalid(source, "invalid query name/header")
		}

		command := queryCommand(fields[1])
		if command == ir.QueryCommand_QUERY_COMMAND_UNSPECIFIED {
			return nil, invalid(source, "unknown query command")
		}

		result = append(result, header{name: fields[0], command: command, start: start, end: end})
	}

	return result, nil
}

func splitQuery(
	parser parse.Parser, file File, item header, end int, tokens []*pg.ScanToken, entries []Entry,
) (*QuerySource, error) {
	first, last := -1, -1
	terminated := false

	for _, token := range tokens {
		start := int(token.GetStart())
		if start < item.end || start >= end || isComment(token) {
			continue
		}

		if terminated {
			source := span(file.Name, file.SQL, start, int(token.GetEnd()))

			return nil, invalid(source, "query must contain exactly one statement")
		}

		if first < 0 {
			first = start
		}

		last = int(token.GetEnd())
		terminated = token.GetToken() == pg.Token_ASCII_59
	}

	if first < 0 {
		return nil, invalid(span(file.Name, file.SQL, item.start, item.end), "empty named query")
	}

	query := &QuerySource{
		Name: item.name, Command: item.command, SQL: file.SQL[first:last],
		Header: span(file.Name, file.SQL, item.start, item.end),
		Meta:   &ir.Metadata{Source: span(file.Name, file.SQL, first, last)},
	}

	for _, entry := range entries {
		if entry.Start < item.end || entry.Start >= end {
			continue
		}

		if entry.Start >= first {
			return nil, invalid(entry.Value.GetSource(), "query annotations must precede SQL")
		}

		if err := validate(parser, entry.Value, Target{Meta: query.Meta}, true); err != nil {
			return nil, err
		}

		entry.Value.Target = &ir.AnnotationValue_QueryName{QueryName: item.name}
		query.Meta.Annotations = append(query.Meta.GetAnnotations(), entry.Value)
	}

	return query, nil
}

func queryCommand(text string) ir.QueryCommand {
	switch text {
	case ":one":
		return ir.QueryCommand_QUERY_COMMAND_ONE
	case ":many":
		return ir.QueryCommand_QUERY_COMMAND_MANY
	case ":exec":
		return ir.QueryCommand_QUERY_COMMAND_EXEC
	case ":execrows":
		return ir.QueryCommand_QUERY_COMMAND_EXEC_ROWS
	case ":copyfrom":
		return ir.QueryCommand_QUERY_COMMAND_COPY_FROM
	default:
		return ir.QueryCommand_QUERY_COMMAND_UNSPECIFIED
	}
}
