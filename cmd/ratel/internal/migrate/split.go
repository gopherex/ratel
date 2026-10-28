package migrate

import (
	"strings"
	"unicode/utf8"
)

// Piece is a run of migration file text: a statement without its trailing
// semicolon, or a comment line between statements.
type Piece struct {
	Text    string
	Comment bool
}

// split cuts SQL into statements and the comment lines between them,
// honoring quotes, dollar quotes and comments inside statements. It needs
// no parser, so statements of any PostgreSQL version split alike.
func split(sql string) []Piece {
	var (
		pieces []Piece
		start  int
	)

	for index := 0; index < len(sql); {
		if strings.TrimSpace(sql[start:index]) == "" && strings.HasPrefix(sql[index:], "--") {
			end := lineEnd(sql, index)
			pieces = append(pieces, Piece{Text: strings.TrimSpace(sql[index:end]), Comment: true})
			index, start = end, end

			continue
		}

		switch next := skip(sql, index); {
		case next > index:
			index = next
		case sql[index] == ';':
			if text := strings.TrimSpace(sql[start:index]); text != "" {
				pieces = append(pieces, Piece{Text: text})
			}

			index++
			start = index
		default:
			index++
		}
	}

	if text := strings.TrimSpace(sql[start:]); text != "" {
		pieces = append(pieces, Piece{Text: text})
	}

	return pieces
}

// skip is the index after a quoted literal, identifier, dollar-quoted string
// or comment starting at index, or index when none starts there.
func skip(sql string, index int) int {
	switch {
	case sql[index] == '\'' || sql[index] == '"':
		return quoted(sql, index, sql[index], false)
	case strings.HasPrefix(sql[index:], "--"):
		return lineEnd(sql, index)
	case strings.HasPrefix(sql[index:], "/*"):
		return blockComment(sql, index)
	case sql[index] == '$':
		return dollarQuoted(sql, index)
	case identStart(sql[index]):
		return word(sql, index)
	default:
		return index
	}
}

// quoted skips a literal or quoted identifier: a doubled quote is an escaped
// quote, and so is a backslash escape in an escape string.
func quoted(sql string, start int, quote byte, backslash bool) int {
	for index := start + 1; index < len(sql); index++ {
		switch {
		case backslash && sql[index] == '\\':
			index++
		case sql[index] != quote:
		case index+1 < len(sql) && sql[index+1] == quote:
			index++
		default:
			return index + 1
		}
	}

	return len(sql)
}

// blockComment skips a comment; PostgreSQL nests block comments.
func blockComment(sql string, start int) int {
	depth := 0

	for index := start; index+1 < len(sql); index++ {
		switch sql[index : index+2] {
		case "/*":
			depth++
			index++
		case "*/":
			depth--
			index++

			if depth == 0 {
				return index + 1
			}
		}
	}

	return len(sql)
}

// word skips an identifier or keyword. Digits and dollar signs continue one,
// so neither opens a dollar quote inside it; a lone E before a quote opens an
// escape string.
func word(sql string, start int) int {
	end := start + 1
	for end < len(sql) && (identStart(sql[end]) || isDigit(sql[end]) || sql[end] == '$') {
		end++
	}

	if end == start+1 && (sql[start] == 'E' || sql[start] == 'e') && end < len(sql) && sql[end] == '\'' {
		return quoted(sql, end, '\'', true)
	}

	return end
}

// identStart reports a byte that starts an identifier: a letter, an
// underscore or any byte of a non-ASCII character.
func identStart(char byte) bool {
	return char == '_' || char >= utf8.RuneSelf || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isDigit(char byte) bool { return char >= '0' && char <= '9' }

// dollarQuoted skips $tag$...$tag$; a $ not opening a tag ($1) is not a quote.
func dollarQuoted(sql string, start int) int {
	end := strings.IndexByte(sql[start+1:], '$')
	if end < 0 {
		return start
	}

	tag := sql[start : start+1+end+1]
	for _, char := range []byte(tag[1 : len(tag)-1]) {
		if !identStart(char) && !isDigit(char) {
			return start
		}
	}

	if len(tag) > len("$$") && isDigit(tag[1]) {
		return start
	}

	if closing := strings.Index(sql[start+len(tag):], tag); closing >= 0 {
		return start + len(tag) + closing + len(tag)
	}

	return len(sql)
}

func lineEnd(sql string, index int) int {
	if end := strings.IndexByte(sql[index:], '\n'); end >= 0 {
		return index + end + 1
	}

	return len(sql)
}
