// Command protoc-gen-ratel is the protoc plugin that turns ratel options in
// .proto files into DDL with ratel annotations. Only the binary lives here;
// the behavior is implemented in internal/cli.
package main

import (
	"os"

	"github.com/gopherex/ratel/cmd/protoc-gen-ratel/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args, os.Stdout, os.Stderr))
}
