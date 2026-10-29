package authoring

import (
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// SchemaStatements exposes schemaStatements.
func SchemaStatements(parser parse.Parser, files []catalog.File) ([]string, error) {
	return schemaStatements(parser, files)
}
