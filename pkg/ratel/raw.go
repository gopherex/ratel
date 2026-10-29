package ratel

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Raw is an SQL predicate fragment of the query's model, the escape hatch for
// what typed columns do not express. Columns are referenced through the
// query's alias, "q"."column". Markers $1..$n refer to args in order and are
// renumbered into the statement; markers inside string literals, quoted
// identifiers, dollar-quoted strings and comments are left alone. A marker
// without an argument renders ErrInvalidQuery.
func Raw[M any](sql string, args ...any) Cond[M] {
	args = append([]any{}, args...)

	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.WriteByte('(')
		renumber(writer, sql, args)
		writer.WriteByte(')')
	}}
}

func renumber(writer *sqlWriter, sql string, args []any) {
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '$' && index+1 < len(sql) && isDigit(sql[index+1]):
			end := index + 1
			for end < len(sql) && isDigit(sql[end]) {
				end++
			}

			number, err := strconv.Atoi(sql[index+1 : end])
			if err != nil || number < 1 || number > len(args) {
				writer.invalid = true

				return
			}

			writer.param(args[number-1])

			index = end
		default:
			end := skipQuoted(sql, index)
			writer.WriteString(sql[index:end])
			index = end
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// skipQuoted returns the end of a literal, identifier, keyword, dollar-quoted
// string or comment starting at index, or index+1 for any other byte. It
// follows the PostgreSQL lexer (scan.l) with standard_conforming_strings on.
func skipQuoted(sql string, index int) int {
	rest := sql[index:]

	switch {
	case strings.HasPrefix(rest, "--"):
		if end := strings.IndexByte(rest, '\n'); end >= 0 {
			return index + end + 1
		}

		return len(sql)
	case strings.HasPrefix(rest, "/*"):
		return blockComment(sql, index)
	case rest[0] == '\'' || rest[0] == '"':
		return closing(sql, index, rest[0], false)
	case rest[0] == '$':
		return dollarQuoted(sql, index)
	case identStart(rest[0]):
		return word(sql, index)
	default:
		return index + 1
	}
}

// blockComment returns the end of a comment starting at index; PostgreSQL
// nests block comments.
func blockComment(sql string, index int) int {
	depth := 0

	for end := index; end+1 < len(sql); end++ {
		switch sql[end : end+2] {
		case "/*":
			depth++
			end++
		case "*/":
			depth--
			end++

			if depth == 0 {
				return end + 1
			}
		}
	}

	return len(sql)
}

// word returns the end of an identifier or keyword. Digits and dollar signs
// continue one, so neither starts a marker or a dollar quote inside it; a
// lone E before a quote opens an escape string.
func word(sql string, index int) int {
	end := index + 1
	for end < len(sql) && (identStart(sql[end]) || isDigit(sql[end]) || sql[end] == '$') {
		end++
	}

	if end == index+1 && (sql[index] == 'E' || sql[index] == 'e') && end < len(sql) && sql[end] == '\'' {
		return closing(sql, end, '\'', true)
	}

	return end
}

// identStart reports a byte that starts an identifier: a letter, an
// underscore or any byte of a non-ASCII character.
func identStart(c byte) bool {
	return c == '_' || c >= utf8.RuneSelf || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// closing finds the end of a quoted literal or identifier: a doubled quote
// is an escaped quote, and so is a backslash escape in an E-prefixed string.
func closing(sql string, index int, quote byte, backslash bool) int {
	for end := index + 1; end < len(sql); end++ {
		switch {
		case backslash && sql[end] == '\\':
			end++
		case sql[end] == quote && end+1 < len(sql) && sql[end+1] == quote:
			end++
		case sql[end] == quote:
			return end + 1
		}
	}

	return len(sql)
}

// dollarQuoted returns the end of $tag$...$tag$, or index+1 when the dollar
// sign does not open one.
func dollarQuoted(sql string, index int) int {
	end := strings.IndexByte(sql[index+1:], '$')
	if end < 0 {
		return index + 1
	}

	tag := sql[index : index+end+2]
	for _, c := range []byte(tag[1 : len(tag)-1]) {
		if !identStart(c) && !isDigit(c) {
			return index + 1
		}
	}

	if body := strings.Index(sql[index+len(tag):], tag); body >= 0 {
		return index + len(tag) + body + len(tag)
	}

	return len(sql)
}
