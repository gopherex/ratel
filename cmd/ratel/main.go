// Command ratel is the host CLI. Only the binary lives here; every command is
// implemented in internal/cli.
package main

import (
	"os"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args, os.Stdout, os.Stderr))
}
