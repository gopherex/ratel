// Package cli implements the protoc-gen-ratel command-line interface.
package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"
)

// Exit codes of the plugin: protoc treats a non-zero exit as a plugin failure.
const (
	exitOK    = 0
	exitUsage = 2
)

// Version returns the module version the plugin was built from: "(devel)" for
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

// Main parses the plugin arguments with the given writers and returns the exit
// code. Only --version is implemented; every other invocation prints usage and
// returns the usage code.
func Main(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("protoc-gen-ratel", flag.ContinueOnError)
	flags.SetOutput(stderr)

	showVersion := flags.Bool("version", false, "print the plugin version and exit")

	if err := flags.Parse(args[1:]); err != nil {
		return exitUsage
	}

	if *showVersion {
		fmt.Fprintln(stdout, Version())

		return exitOK
	}

	usage(stderr)

	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: protoc-gen-ratel --version")
}
