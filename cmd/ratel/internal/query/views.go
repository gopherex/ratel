package query

import (
	"strconv"
	"strings"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// Views sets the output of every view and materialized view of the catalog:
// the result columns of SELECT * from it, inferred like a named query. It is
// the one mutation of the catalog, made by the host before plugins run.
func Views(parser parse.Parser, cat *ir.Catalog, builtins []*ir.Schema) error {
	var (
		sources []*annotate.QuerySource
		targets []*[]*ir.QueryColumn
	)

	add := func(name *ir.QualifiedName, meta *ir.Metadata, output *[]*ir.QueryColumn) {
		quoted := make([]string, 0, len(name.GetParts()))
		for _, part := range name.GetParts() {
			quoted = append(quoted, `"`+strings.ReplaceAll(part, `"`, `""`)+`"`)
		}

		sources = append(sources, &annotate.QuerySource{
			Name: "View" + strconv.Itoa(len(sources)), SQL: "SELECT * FROM " + strings.Join(quoted, "."),
			Command: ir.QueryCommand_QUERY_COMMAND_MANY, Meta: &ir.Metadata{Source: meta.GetSource()},
		})

		targets = append(targets, output)
	}

	for _, schema := range cat.GetSchemas() {
		for _, view := range schema.GetViews() {
			add(view.GetName(), view.GetMeta(), &view.Output)
		}

		for _, view := range schema.GetMaterializedViews() {
			add(view.GetName(), view.GetMeta(), &view.Output)
		}
	}

	inferred, err := Infer(parser, cat, builtins, sources)
	if err != nil {
		return err
	}

	for index, value := range inferred {
		*targets[index] = value.GetColumns()
	}

	return nil
}
