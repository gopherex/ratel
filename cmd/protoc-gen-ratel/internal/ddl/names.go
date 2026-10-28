package ddl

import (
	"strings"
	"unicode"
)

// defaultSchema is the schema of a file that sets no File.schema.
const defaultSchema = "public"

// snakeCase converts a proto name to its default SQL name: "UserID" becomes
// "user_id", "HTTPServer" becomes "http_server".
func snakeCase(name string) string {
	runes := []rune(name)

	var out strings.Builder

	for index, char := range runes {
		if unicode.IsUpper(char) && index > 0 && startsWord(runes, index) {
			out.WriteByte('_')
		}

		out.WriteRune(unicode.ToLower(char))
	}

	return out.String()
}

// startsWord reports whether the upper-case rune at index begins a word: it
// follows a lower-case letter or digit, or it ends an acronym.
func startsWord(runes []rune, index int) bool {
	prev := runes[index-1]
	if unicode.IsLower(prev) || unicode.IsDigit(prev) {
		return true
	}

	return unicode.IsUpper(prev) && index+1 < len(runes) && unicode.IsLower(runes[index+1])
}

// quoteIdent renders a SQL identifier. Every identifier is quoted, so reserved
// words and mixed case need no special cases; quoting a lower-case simple name
// yields the same catalog identity as writing it bare.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// qualify renders a schema-qualified object name.
func qualify(schema, name string) string {
	return quoteIdent(schema) + "." + quoteIdent(name)
}

// sqlString renders a SQL string literal.
func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// joinIdents renders quoted identifiers as a comma-separated list.
func joinIdents(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, quoteIdent(name))
	}

	return strings.Join(quoted, ", ")
}

// exported reports whether name is an exported Go-style identifier, the form
// ratel requires for relation and row names.
func exported(name string) bool {
	runes := []rune(name)
	if len(runes) == 0 || !unicode.IsUpper(runes[0]) {
		return false
	}

	for _, char := range runes {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return false
		}
	}

	return true
}
