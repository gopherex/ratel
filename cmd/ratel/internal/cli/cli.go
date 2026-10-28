// Package cli implements the ratel command-line interface.
package cli

import (
	"fmt"
	"io"
	"runtime/debug"
)

// Exit codes of the ratel command (docs/cli.md).
const (
	exitOK    = 0
	exitUsage = 2
	// program name plus one command.
	minArgs = 2
)

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
// its exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) < minArgs {
		usage(stderr)

		return exitUsage
	}

	switch args[1] {
	case "init":
		return initProject(args[2:], stdout, stderr)
	case "check":
		return check(args[2:], stderr)
	case "generate":
		return generate(args[2:], stderr)
	case "dump":
		return dump(args[2:], stdout, stderr)
	case "migrate":
		return migrateCommand(args[2:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, Version())

		return exitOK
	default:
		fmt.Fprintf(stderr, "ratel: unknown command %q\n", args[1])
		usage(stderr)

		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: ratel <command> [flags]")
	fmt.Fprintln(w, "commands: version, init, check, generate, dump, migrate")
}
