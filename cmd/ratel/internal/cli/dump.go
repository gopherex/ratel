package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/ir"
	sdk "github.com/gopherex/ratel/pkg/plugin"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	host "github.com/gopherex/ratel/cmd/ratel/internal/plugin"
)

func dump(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("dump", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "ratel.yaml", "configuration file")
	format := flags.String("format", "binpb", "binpb or json")
	name := flags.String("plugin", "", "dump the negotiated request for this plugin")

	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ratel dump: unexpected positional arguments")

		return exitUsage
	}

	encoding := sdk.Format(*format)
	if encoding != sdk.Binary && encoding != sdk.JSON {
		fmt.Fprintln(stderr, "ratel dump: format must be binpb or json")

		return exitUsage
	}

	if err := dumpCatalog(*configPath, *name, encoding, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "ratel dump:", err)

		return 1
	}

	return exitOK
}

func dumpCatalog(configPath, name string, format sdk.Format, stdout, stderr io.Writer) error {
	project, err := loadProject(configPath)
	if err != nil {
		return err
	}

	for _, warning := range project.warnings {
		fmt.Fprintf(stderr, "ratel dump: warning %s: %s\n", warning.GetCode(), warning.GetMessage())
	}

	if name == "" {
		return sdk.Write(stdout, format, project.catalog)
	}

	entry, err := findPlugin(project.settings.Plugins, name, project.root)
	if err != nil {
		return err
	}

	input, err := project.request(entry)
	if err != nil {
		return err
	}

	runner := host.New(generators())
	defer runner.Close(context.Background())

	request, err := runner.Prepare(context.Background(), entry, input)
	if err != nil {
		return err
	}

	return sdk.Write(stdout, format, request)
}

func schemaFiles(root string, patterns []string) ([]catalog.File, error) {
	names := make(map[string]bool)

	for _, pattern := range patterns {
		matches, err := doublestar.FilepathGlob(filepath.Join(root, pattern), doublestar.WithFailOnIOErrors())
		if err != nil {
			return nil, fmt.Errorf("schema glob %q: %w", pattern, err)
		}

		if len(matches) == 0 {
			return nil, &config.Error{Message: "schema pattern matched no files: " + pattern}
		}

		for _, name := range matches {
			names[name] = true
		}
	}

	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}

	slices.Sort(ordered)
	files := make([]catalog.File, 0, len(ordered))

	for _, name := range ordered {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read schema: %w", err)
		}

		relative, err := filepath.Rel(root, name)
		if err != nil {
			return nil, fmt.Errorf("schema path: %w", err)
		}

		files = append(files, catalog.File{Name: filepath.ToSlash(relative), SQL: string(data)})
	}

	return files, nil
}

func findPlugin(entries []config.Plugin, name, root string) (config.Plugin, error) {
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}

		if config.IsPath(entry.Exec) && !filepath.IsAbs(entry.Exec) {
			entry.Exec = filepath.Join(root, entry.Exec)
		}

		if entry.Wasm != "" && !filepath.IsAbs(entry.Wasm) && !isURL(entry.Wasm) {
			entry.Wasm = filepath.Join(root, entry.Wasm)
		}

		return entry, nil
	}

	return config.Plugin{}, &config.Error{Message: "plugin not configured: " + name}
}

func isURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func annotations(message protoreflect.Message, namespace string) []*ir.AnnotationValue {
	var result []*ir.AnnotationValue

	fields := message.Descriptor().Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		if field.Message() == nil || !message.Has(field) {
			continue
		}

		value := message.Get(field)
		visit := func(child protoreflect.Message) {
			switch annotation := child.Interface().(type) {
			case *ir.AnnotationValue:
				if annotation.GetNamespace() == namespace {
					copyValue := new(ir.AnnotationValue)
					proto.Merge(copyValue, annotation)
					result = append(result, copyValue)
				}

				return
			default:
			}

			result = append(result, annotations(child, namespace)...)
		}

		if field.IsList() {
			list := value.List()
			for index := range list.Len() {
				visit(list.Get(index).Message())
			}
		} else {
			visit(value.Message())
		}
	}

	return result
}
