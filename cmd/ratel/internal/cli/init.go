package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// errConfigured is a directory that already has a ratel.yaml.
var errConfigured = errors.New("ratel.yaml exists")

// configTemplate is the ratel.yaml init writes; %s is the plugins section.
const configTemplate = `version: 1

postgres:
  version: 18                 # production major version: inference and recipes target it

schema: [schema/**/*.sql]     # CREATE statements, in any order and any number of files
queries: []                   # query files grouped into Go packages, e.g.:
#  - files: [queries/users/*.sql]
#    package: users
migrations: migrations        # planned and hand-written migration files, and ratel.sum

dev:
  image: postgres:18-alpine   # disposable server for plans and probes; or: url: postgres://...

%s
migrate:
  lock_timeout: 100ms         # first attempt; the retry schedule grows it
  retry_schedule: gitlab      # the GitLab with_lock_retries table without its final no-timeout attempt
  max_total_wait: 10m         # exhausted: the step fails and prints blocking sessions
  statement_timeout: 5s       # short DDL steps
  long_tx_threshold: 1m       # pre-flight: transactions older than this block lock-taking steps
  backfill:
    batch: 1000
    pause: 50ms
    max_replica_lag: 10s
`

// pluginsSection configures the Go generator for a module.
const pluginsSection = `plugins:
  - name: go
    builtin: true
    out: internal/db
    options:
      import_path: %s/internal/db
`

// noPlugins leaves the Go generator as an example without a module.
const noPlugins = `plugins: []                   # the Go generator, once the module path is known:
#  - name: go
#    builtin: true
#    out: internal/db
#    options:
#      import_path: github.com/acme/app/internal/db
`

// starterSchema keeps the schema pattern matching a file.
const starterSchema = "-- CREATE SCHEMA, CREATE TABLE and other DDL of the application.\n"

// initProject writes ratel.yaml and the source directories.
func initProject(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	directory := flags.String("dir", ".", "project directory")

	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitUsage
	}

	if err := scaffold(*directory); err != nil {
		fmt.Fprintln(stderr, "ratel init:", err)

		return exitDiagnostics
	}

	fmt.Fprintln(stdout, "wrote", filepath.Join(*directory, "ratel.yaml"))

	return exitOK
}

func scaffold(directory string) error {
	config := filepath.Join(directory, "ratel.yaml")
	if _, err := os.Stat(config); err == nil {
		return fmt.Errorf("%w in %s", errConfigured, directory)
	}

	for _, sub := range []string{"schema", "queries", "migrations"} {
		if err := os.MkdirAll(filepath.Join(directory, sub), directoryMode); err != nil {
			return fmt.Errorf("create %s: %w", sub, err)
		}
	}

	starter := filepath.Join(directory, "schema", "schema.sql")
	if _, err := os.Stat(starter); errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(starter, []byte(starterSchema), fileMode); err != nil {
			return fmt.Errorf("write %s: %w", starter, err)
		}
	}

	plugins := noPlugins
	if module := modulePath(directory); module != "" {
		plugins = fmt.Sprintf(pluginsSection, module)
	}

	if err := atomicWrite(config, fmt.Appendf(nil, configTemplate, plugins), fileMode); err != nil {
		return fmt.Errorf("write ratel.yaml: %w", err)
	}

	return nil
}

// modulePath is the module of the go.mod in a directory, or empty.
func modulePath(directory string) string {
	data, err := os.ReadFile(filepath.Join(directory, "go.mod"))
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(module), `"`)
		}
	}

	return ""
}
