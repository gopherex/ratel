// Package cli implements the protoc-gen-ratel command-line interface.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/gopherex/ratel/cmd/protoc-gen-ratel/internal/ddl"
)

// Exit codes of the plugin: protoc treats a non-zero exit as a plugin failure.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Errors of a plugin run that are reported to protoc in the response.
var (
	ErrRequest   = errors.New("malformed CodeGeneratorRequest")
	ErrParameter = errors.New("protoc-gen-ratel takes no parameters")
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

// Main runs the plugin. With no arguments, as protoc invokes it, Main reads a
// CodeGeneratorRequest from stdin and writes a CodeGeneratorResponse to stdout.
// --version prints the version. Any other argument prints usage.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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

	if flags.NArg() > 0 {
		usage(stderr)

		return exitUsage
	}

	if err := generate(stdin, stdout); err != nil {
		fmt.Fprintln(stderr, err)

		return exitFailure
	}

	return exitOK
}

// generate runs one protoc request. Generation errors travel in the response,
// so protoc reports them with the plugin's own message.
func generate(stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}

	request := new(pluginpb.CodeGeneratorRequest)
	if err := proto.Unmarshal(raw, request); err != nil {
		return fmt.Errorf("%w: %w", ErrRequest, err)
	}

	plugin, err := protogen.Options{}.New(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRequest, err)
	}

	if request.GetParameter() != "" {
		plugin.Error(ErrParameter)
	} else if err := ddl.Generate(plugin); err != nil {
		plugin.Error(err)
	}

	response, err := proto.Marshal(plugin.Response())
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	if _, err := stdout.Write(response); err != nil {
		return fmt.Errorf("write response: %w", err)
	}

	return nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: protoc-gen-ratel [--version]  (protoc passes the request on stdin)")
}
