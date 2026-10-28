package plan

// Exposed internals.
func BoundCheck(key []string, bound string) (string, bool) { return boundCheck(key, bound) }
func Generated(name, suffix string) string                 { return generated(name, suffix) }
func LastPart(name string) string                          { return lastPart(name) }
func SchemaOf(name string) string                          { return schemaOf(name) }
func Unquote(identifier string) string                     { return unquote(identifier) }
func Quote(identifier string) string                       { return quote(identifier) }
func ReplaceIdentifier(sql, identifier, with string) string {
	return replaceIdentifier(sql, identifier, with)
}
func Mentions(sql, identifier string) bool            { return mentions(sql, identifier) }
func ConstraintDefinition(sql, name string) string    { return constraintDefinition(sql, name) }
func Referenced(sql string) string                    { return referenced(sql) }
func NotNullName(table, column, clause string) string { return notNullName(table, column, clause) }

// UniqueDefinition is uniqueDefinition with exported fields.
type UniqueDefinition struct {
	Keyword           string
	Columns           []string
	Index, Deferrable string
}

// UniqueParts exposes uniqueParts.
func UniqueParts(definition string) (UniqueDefinition, bool) {
	parts, ok := uniqueParts(definition)

	return UniqueDefinition{parts.keyword, parts.columns, parts.index, parts.deferrable}, ok
}
