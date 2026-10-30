// Package cli implements the ratel command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Exit codes of the ratel command (docs/cli.md).
const (
	exitOK          = 0
	exitDiagnostics = 1
	exitUsage       = 2
)

// commandError is a command that ran and failed (exit 1). Any other error
// cobra returns, such as an unknown flag or a missing operand, is a usage
// error (exit 2).
type commandError struct{ err error }

func (e commandError) Error() string { return e.err.Error() }

func (e commandError) Unwrap() error { return e.err }

// failure marks err, when set, as the failure of a command that ran.
func failure(err error) error {
	if err == nil {
		return nil
	}

	return commandError{err: err}
}

// Version returns the module version the binary was built from: "(devel)" for
// a go build or go run without a version stamp.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}

	return VersionOf(info)
}

// VersionOf picks the main module version out of build information.
func VersionOf(info *debug.BuildInfo) string {
	if info == nil || info.Main.Version == "" {
		return "(devel)"
	}

	return info.Main.Version
}

// Main runs the ratel command with the given arguments and writers and returns
// its exit code. An interrupt cancels the running command.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := rootCommand(stdout, stderr)
	root.SetArgs(args[1:])

	command, err := root.ExecuteContextC(ctx)

	var failed commandError

	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &failed):
		fmt.Fprintf(stderr, "%s: %v\n", command.CommandPath(), failed.err)

		return exitDiagnostics
	default:
		fmt.Fprintf(stderr, "%s: %v\nRun '%s --help' for usage.\n", command.CommandPath(), err, command.CommandPath())

		return exitUsage
	}
}

func rootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "ratel",
		Short: "PostgreSQL data access for Go: code from SQL and migrations that never lock",
		Long: `ratel generates typed Go code from PostgreSQL schema and query files and
plans migrations from the schema files, so that applying them never blocks
traffic. Every command reads ratel.yaml; docs: https://gopherex.github.io/ratel/`,
		Version:       Version(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(versionCommand(), initCommand(), checkCommand(), generateCommand(), dumpCommand(),
		migrateCommand())

	return root
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version the binary was built from",
		Args:  cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintln(command.OutOrStdout(), Version())
		},
	}
}

// configFlag adds --config to command.
func configFlag(command *cobra.Command) *string {
	return command.Flags().String("config", "ratel.yaml", "configuration file")
}
