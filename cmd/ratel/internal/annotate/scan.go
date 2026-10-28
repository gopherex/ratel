// Package annotate scans SQL comments and validates attached annotation metadata.
package annotate

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// File is a complete SQL source, including its diagnostic filename.
type File struct{ Name, SQL string }

// Entry retains the containing comment range as well as the annotation position.
type Entry struct {
	Value      *ir.AnnotationValue
	Start, End int
}

// Error is an invalid annotation or query declaration, with a stable diagnostic.
type Error struct{ Diagnostic *ir.Diagnostic }

func (e *Error) Error() string {
	source := e.Diagnostic.GetSource()

	return fmt.Sprintf("%s:%d:%d: %s: %s", source.GetFile(), source.GetStartLine(),
		source.GetStartColumn(), e.Diagnostic.GetCode(), e.Diagnostic.GetMessage())
}

func invalid(source *ir.SourceSpan, message string) error {
	return &Error{Diagnostic: &ir.Diagnostic{
		Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR,
		Code:     "RTL-A01", Message: message, Source: source,
	}}
}

type position struct{ line, column uint32 }

type lexeme struct {
	value, rest string
	quoted      bool
}

func span(file, sql string, start, end int) *ir.SourceSpan {
	start = max(0, min(start, len(sql)))
	end = max(start, min(end, len(sql)))
	first, last := coordinate(sql[:start]), coordinate(sql[:end])

	return &ir.SourceSpan{
		File: file, StartLine: first.line, StartColumn: first.column,
		EndLine: last.line, EndColumn: last.column,
		StartOffset: uint64(start), EndOffset: uint64(end),
	}
}

func coordinate(prefix string) position {
	result := position{line: 1, column: 1}

	for _, char := range prefix {
		if char == '\n' {
			result.line++
			result.column = 1
		} else {
			result.column++
		}
	}

	return result
}

func scanSQL(parser parse.Parser, file, sql string) ([]*pg.ScanToken, error) {
	if len(sql) > math.MaxInt32 {
		return nil, invalid(&ir.SourceSpan{File: file, StartLine: 1, StartColumn: 1}, "SQL exceeds scanner offset limit")
	}

	result, err := parser.Scan(sql)
	if err != nil {
		return nil, fmt.Errorf("%s: scan SQL: %w", file, err)
	}

	return result.GetTokens(), nil
}

func isComment(token *pg.ScanToken) bool {
	return token.GetToken() == pg.Token_SQL_COMMENT || token.GetToken() == pg.Token_C_COMMENT
}

// Scan reads annotations only from real PostgreSQL comment tokens.
// Parser shares the application concurrency bound with parsing and deparsing.
func Scan(parser parse.Parser, file, sql string) ([]Entry, error) {
	tokens, err := scanSQL(parser, file, sql)
	if err != nil {
		return nil, err
	}

	return scanAnnotations(file, sql, tokens)
}

func scanAnnotations(file, sql string, tokens []*pg.ScanToken) ([]Entry, error) {
	result := make([]Entry, 0)

	for _, token := range tokens {
		if !isComment(token) {
			continue
		}

		start, end := int(token.GetStart()), int(token.GetEnd())

		body := sql[start+2 : end]
		if token.GetToken() == pg.Token_C_COMMENT {
			body = strings.TrimSuffix(body, "*/")
		}

		entries, err := scanComment(file, sql, body, start, end)
		if err != nil {
			return nil, err
		}

		result = append(result, entries...)
	}

	return result, nil
}

func scanComment(file, sql, body string, start, end int) ([]Entry, error) {
	var entries []Entry

	offset := start + len("--")

	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))

		if strings.HasPrefix(trimmed, "@") {
			location := offset + strings.Index(line, trimmed)
			source := span(file, sql, location, location+len(trimmed))

			value, err := parseAnnotation(trimmed, source)
			if err != nil {
				return nil, err
			}

			entries = append(entries, Entry{Value: value, Start: start, End: end})
		}

		offset += len(line)
	}

	return entries, nil
}

func parseAnnotation(text string, source *ir.SourceSpan) (*ir.AnnotationValue, error) {
	head, rest := text, ""
	// Tabs and newlines are argument separators too.
	if index := strings.IndexFunc(head, unicode.IsSpace); index >= 0 {
		head, rest = text[:index], text[index:]
	}

	namespace, name, ok := strings.Cut(strings.TrimPrefix(head, "@"), ".")
	if !ok || !identifier(namespace) || !identifier(name) {
		return nil, invalid(source, "malformed annotation name")
	}

	args, err := arguments(rest, source)
	if err != nil {
		return nil, err
	}

	return &ir.AnnotationValue{Namespace: namespace, Name: name, Args: args, Source: source}, nil
}

func arguments(text string, source *ir.SourceSpan) ([]*ir.AnnotationArg, error) {
	var result []*ir.AnnotationArg

	keys := make(map[string]bool)

	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		parsed, err := word(text, source)
		if err != nil {
			return nil, err
		}

		value, rest := parsed.value, parsed.rest
		argument := &ir.AnnotationArg{Value: value}

		if strings.HasPrefix(rest, "=") {
			if parsed.quoted || !identifier(value) {
				return nil, invalid(source, "invalid argument key")
			}

			if keys[value] {
				return nil, invalid(source, "duplicate argument key "+value)
			}

			keys[value] = true
			argument.Key = value

			parsed, err = word(rest[1:], source)
			if err != nil {
				return nil, err
			}

			argument.Value, rest = parsed.value, parsed.rest
		}

		if rest != "" && !startsSpace(rest) {
			return nil, invalid(source, "expected whitespace after value")
		}

		result = append(result, argument)
		text = rest
	}

	return result, nil
}

func word(text string, source *ir.SourceSpan) (lexeme, error) {
	if text == "" || text[0] == '=' || startsSpace(text) {
		return lexeme{}, invalid(source, "missing annotation value")
	}

	if text[0] != '"' {
		index := strings.IndexFunc(text, func(char rune) bool {
			return unicode.IsSpace(char) || char == '"' || char == '='
		})
		if index < 0 {
			return lexeme{value: text}, nil
		}

		return lexeme{value: text[:index], rest: text[index:]}, nil
	}

	var value strings.Builder

	for index := 1; index < len(text); index++ {
		if text[index] != '"' {
			value.WriteByte(text[index])

			continue
		}

		if index+1 < len(text) && text[index+1] == '"' {
			value.WriteByte('"')

			index++

			continue
		}

		return lexeme{value: value.String(), rest: text[index+1:], quoted: true}, nil
	}

	return lexeme{}, invalid(source, "unterminated quoted value")
}

func startsSpace(text string) bool {
	first, _ := utf8.DecodeRuneInString(text)

	return unicode.IsSpace(first)
}

func identifier(text string) bool {
	if text == "" {
		return false
	}

	for index, char := range text {
		if char != '_' && !unicode.IsLetter(char) && (index == 0 || !unicode.IsDigit(char)) {
			return false
		}
	}

	return true
}
