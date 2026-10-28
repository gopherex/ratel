package migrate

import "strings"

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
		return quoted(sql, index, sql[index])
	case strings.HasPrefix(sql[index:], "--"):
		return lineEnd(sql, index)
	case strings.HasPrefix(sql[index:], "/*"):
		if end := strings.Index(sql[index:], "*/"); end >= 0 {
			return index + end + len("*/")
		}

		return len(sql)
	case sql[index] == '$':
		return dollarQuoted(sql, index)
	default:
		return index
	}
}

func quoted(sql string, start int, quote byte) int {
	for index := start + 1; index < len(sql); index++ {
		if sql[index] != quote {
			continue
		}

		if index+1 < len(sql) && sql[index+1] == quote {
			index++

			continue
		}

		return index + 1
	}

	return len(sql)
}

// dollarQuoted skips $tag$...$tag$; a $ not opening a tag ($1) is not a quote.
func dollarQuoted(sql string, start int) int {
	end := strings.IndexByte(sql[start+1:], '$')
	if end < 0 {
		return start
	}

	tag := sql[start : start+1+end+1]
	for _, char := range tag[1 : len(tag)-1] {
		if char != '_' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return start
		}
	}

	if len(tag) > len("$$") && tag[1] >= '0' && tag[1] <= '9' {
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
