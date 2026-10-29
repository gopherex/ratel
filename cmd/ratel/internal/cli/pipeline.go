package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/gopherex/ratel/pkg/ir"
	sdk "github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/query"
)

// generators are the built-in output plugins, by their configured name.
func generators() map[string]sdk.Generator {
	return map[string]sdk.Generator{gengo.Name: gengo.Generator{}}
}

// project is the shared pipeline result: schema catalog plus inferred queries.
type project struct {
	root     string
	settings config.Config
	catalog  *ir.Catalog
	queries  []*ir.Query
	// packages maps a query name to the Go package of its configured group.
	packages map[string]string
	warnings []*ir.Diagnostic
}

// loadProject parses the schema, builds the catalog and infers every configured query.
func loadProject(configPath string) (project, error) {
	settings, err := config.Load(configPath)
	if err != nil {
		return project{}, err
	}

	parser, err := parse.New(settings.Parser)
	if err != nil {
		return project{}, err
	}

	root, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return project{}, fmt.Errorf("configuration directory: %w", err)
	}

	files, err := schemaFiles(root, settings.Schema)
	if err != nil {
		return project{}, err
	}

	snapshot, err := builtins.For(settings.Postgres.Version)
	if err != nil {
		return project{}, err
	}

	owned := make([]string, 0, len(settings.Plugins))
	for _, entry := range settings.Plugins {
		owned = append(owned, entry.Name)
	}

	result, err := catalog.Build(parser, files, settings.Postgres.Version, snapshot, owned...)
	if err != nil {
		return project{}, err
	}

	if err := query.Views(parser, result, snapshot); err != nil {
		return project{}, err
	}

	queryFiles, groups, err := queryGroups(root, settings.Queries)
	if err != nil {
		return project{}, err
	}

	set, err := annotate.Queries(parser, queryFiles, owned)
	if err != nil {
		return project{}, err
	}

	inferred, err := query.Infer(parser, result, snapshot, set.Queries)
	if err != nil {
		return project{}, err
	}

	packages := make(map[string]string, len(inferred))
	for _, value := range inferred {
		packages[value.GetName()] = groups[value.GetMeta().GetSource().GetFile()]
	}

	return project{
		root: root, settings: settings, catalog: result, queries: inferred, packages: packages,
		warnings: set.Diagnostics,
	}, nil
}

// queryGroups reads every configured query file once and records the Go package of each file.
func queryGroups(root string, groups []config.Query) ([]annotate.File, map[string]string, error) {
	var files []annotate.File

	packages := make(map[string]string)

	for _, group := range groups {
		matched, err := schemaFiles(root, group.Files)
		if err != nil {
			return nil, nil, err
		}

		for _, file := range matched {
			if previous, ok := packages[file.Name]; ok {
				return nil, nil, &config.Error{Message: fmt.Sprintf(
					"query file %s belongs to packages %s and %s", file.Name, previous, group.Package,
				)}
			}

			packages[file.Name] = group.Package

			files = append(files, annotate.File{Name: file.Name, SQL: file.SQL})
		}
	}

	return files, packages, nil
}

// request builds the Generate payload for one configured plugin.
func (p *project) request(entry config.Plugin) (*pluginpb.GenerateRequest, error) {
	options, err := json.Marshal(entry.Options)
	if err != nil {
		return nil, fmt.Errorf("encode plugin options: %w", err)
	}

	input := &pluginpb.GenerateRequest{
		Catalog: p.catalog, Queries: p.queries, HostVersion: Version(), Options: options,
	}
	input.Annotations = annotations(p.catalog.ProtoReflect(), entry.Name)

	for _, value := range p.queries {
		name := p.packages[value.GetName()]
		input.Annotations = append(input.Annotations, &ir.AnnotationValue{
			Namespace: entry.Name, Name: "package",
			Args:   []*ir.AnnotationArg{{Value: name}},
			Target: &ir.AnnotationValue_QueryName{QueryName: value.GetName()},
		})
	}

	return input, nil
}
