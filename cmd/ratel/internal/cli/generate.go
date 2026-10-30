package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gopherex/ratel/pkg/ir"
	sdk "github.com/gopherex/ratel/pkg/plugin"

	host "github.com/gopherex/ratel/cmd/ratel/internal/plugin"
)

// errGenerationFailed reports a plugin response with ERROR diagnostics,
// which are printed before it.
var errGenerationFailed = errors.New("generation failed")

func checkCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "check",
		Short: "Parse all sources, build the catalog and infer the queries",
		Long: `check parses the schema and query files, builds the catalog, infers views
and queries and validates annotations. It prints warnings; an error fails it.`,
		Args: cobra.NoArgs,
	}
	configPath := configFlag(command)

	command.RunE = func(command *cobra.Command, _ []string) error {
		project, err := loadProject(*configPath)
		if err != nil {
			return failure(err)
		}

		printDiagnostics(command.ErrOrStderr(), project.warnings)

		return nil
	}

	return command
}

func generateCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "generate",
		Short: "Run check, then every configured plugin, and write their files",
		Long: `generate does what check does, then runs every plugin of ratel.yaml in
order and writes its files under its out directory, relative to the
configuration file. Files a previous run wrote and this one did not are
deleted.`,
		Args: cobra.NoArgs,
	}
	configPath := configFlag(command)

	command.RunE = func(command *cobra.Command, _ []string) error {
		project, err := loadProject(*configPath)
		if err != nil {
			return failure(err)
		}

		stderr := command.ErrOrStderr()
		printDiagnostics(stderr, project.warnings)

		ctx := command.Context()
		runner := host.New(generators())

		defer runner.Close(context.WithoutCancel(ctx))

		for _, entry := range project.settings.Plugins {
			if err := project.runPlugin(ctx, runner, entry.Name, stderr); err != nil {
				return failure(fmt.Errorf("plugin %s: %w", entry.Name, err))
			}
		}

		return nil
	}

	return command
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
