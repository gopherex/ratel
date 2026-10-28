package gengo

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jinzhu/inflection"
)

// initialism returns the Go spelling of a word that is an initialism.
func initialism(word string) (string, bool) {
	switch word {
	case "id", "url", "uri", "api", "sql", "http", "https", "json", "uuid", "ip", "xml", "html", "db", "tcp", "udp":
		return strings.ToUpper(word), true
	default:
		return "", false
	}
}

// pascal converts a snake_case or camelCase name to PascalCase with Go
// initialisms: "manager_id" -> "ManagerID".
func pascal(name string) string {
	var out strings.Builder

	for _, word := range words(name) {
		lower := strings.ToLower(word)
		if upper, ok := initialism(lower); ok {
			out.WriteString(upper)
		} else {
			out.WriteString(capitalize(lower))
		}
	}

	return out.String()
}

// camel is pascal with a lower-case first word, for unexported identifiers.
func camel(name string) string {
	parts := words(name)
	if len(parts) == 0 {
		return ""
	}

	return strings.ToLower(parts[0]) + pascal(strings.Join(parts[1:], "_"))
}

// singular and plural name a model and its descriptor: "users" -> "User",
// "Users".
func singular(name string) string { return pascal(inflection.Singular(name)) }

func plural(name string) string { return pascal(inflection.Plural(name)) }

// words splits on underscores and camelCase transitions:
// "getUserID" -> ["get", "User", "ID"].
func words(name string) []string {
	var out []string

	for part := range strings.SplitSeq(name, "_") {
		if part != "" {
			out = append(out, splitCamel(part)...)
		}
	}

	return out
}

func splitCamel(value string) []string {
	runes := []rune(value)

	var (
		out   []string
		start int
	)

	for index := 1; index < len(runes); index++ {
		if !unicode.IsUpper(runes[index]) {
			continue
		}

		lowerBefore := unicode.IsLower(runes[index-1])
		acronymEnd := index+1 < len(runes) && unicode.IsLower(runes[index+1]) && index-start > 1

		if lowerBefore || acronymEnd {
			out = append(out, string(runes[start:index]))
			start = index
		}
	}

	return append(out, string(runes[start:]))
}

func capitalize(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}

	runes[0] = unicode.ToUpper(runes[0])

	return string(runes)
}

// packageName is a schema or query group as a Go package name.
func packageName(name string) string {
	var out strings.Builder

	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		}
	}

	result := out.String()
	if first, _ := utf8.DecodeRuneInString(result); result == "" || unicode.IsDigit(first) {
		result = "p" + result
	}

	return result
}
