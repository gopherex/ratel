package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/gopherex/ratel/ir"
	sdk "github.com/gopherex/ratel/plugin"

	host "github.com/gopherex/ratel/cmd/ratel/internal/plugin"
)

const exitDiagnostics = 1

// errGenerationFailed reports a plugin response with ERROR diagnostics,
// which are printed before it.
var errGenerationFailed = errors.New("generation failed")

// check parses every source, builds the catalog and infers the queries.
func check(args []string, stderr io.Writer) int {
	configPath, ok := commandFlags("check", args, stderr)
	if !ok {
		return exitUsage
	}

	project, err := loadProject(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "ratel check:", err)

		return exitDiagnostics
	}

	printDiagnostics(stderr, project.warnings)

	return exitOK
}

// generate runs check, then every configured plugin, and writes their files
// under each plugin's output directory.
func generate(args []string, stderr io.Writer) int {
	configPath, ok := commandFlags("generate", args, stderr)
	if !ok {
		return exitUsage
	}

	project, err := loadProject(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "ratel generate:", err)

		return exitDiagnostics
	}

	printDiagnostics(stderr, project.warnings)

	runner := host.New(generators())
	defer runner.Close(context.Background())

	for _, entry := range project.settings.Plugins {
		if err := project.runPlugin(context.Background(), runner, entry.Name, stderr); err != nil {
			fmt.Fprintf(stderr, "ratel generate: plugin %s: %v\n", entry.Name, err)

			return exitDiagnostics
		}
	}

	return exitOK
}

func (p *project) runPlugin(ctx context.Context, runner *host.Host, name string, stderr io.Writer) error {
	entry, err := findPlugin(p.settings.Plugins, name, p.root)
	if err != nil {
		return err
	}

	input, err := p.request(entry)
	if err != nil {
		return err
	}

	out := entry.Out
	if !filepath.IsAbs(out) {
		out = filepath.Join(p.root, out)
	}

	input.OutDir = out

	response, err := runner.Run(ctx, entry, input)
	if response != nil {
		printDiagnostics(stderr, response.GetDiagnostics())
	}

	if err != nil {
		return err
	}

	if sdk.HasErrors(response) {
		return errGenerationFailed
	}

	return writeOutput(out, response.GetGenerate().GetFiles())
}

func commandFlags(command string, args []string, stderr io.Writer) (string, bool) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "ratel.yaml", "configuration file")

	if err := flags.Parse(args); err != nil {
		return "", false
	}

	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "ratel %s: unexpected positional arguments\n", command)

		return "", false
	}

	return *configPath, true
}

// printDiagnostics prints file:line:column: severity CODE: message.
func printDiagnostics(stderr io.Writer, diagnostics []*ir.Diagnostic) {
	for _, diagnostic := range diagnostics {
		source := diagnostic.GetSource()

		severity := "warning"
		if diagnostic.GetSeverity() == ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR {
			severity = "error"
		}

		fmt.Fprintf(stderr, "%s:%d:%d: %s %s: %s\n", source.GetFile(), source.GetStartLine(), source.GetStartColumn(),
			severity, diagnostic.GetCode(), diagnostic.GetMessage())
	}
}
