package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/ident"
	"github.com/gopherex/ratel/cmd/ratel/internal/migrate"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Migrate errors.
var (
	// errFindings is a plan or lint run with findings that fail it.
	errFindings = errors.New("findings fail the migration")
	// errUnknownCommand is a subcommand migrateFlags did not accept.
	errUnknownCommand = errors.New("unknown subcommand")
)

// Subcommands that take operands.
const (
	planCommand = "plan"
	lintCommand = "lint"
)

// workspace is what every migrate command reads: the configuration, the
// desired catalog and the migration files.
type workspace struct {
	settings  config.Config
	parser    parse.Parser
	schema    []catalog.File
	catalog   *ir.Catalog
	directory string
	sources   []migrate.Source
	quoter    *ident.Quoter
	stdout    io.Writer
}

// arguments are a migrate subcommand's flags and operands.
type arguments struct {
	command, config, url string
	operands             []string
}

// migrateUsage names the subcommands.
const migrateUsage = "usage: ratel migrate plan <name> | lint [<file>...] | verify | " +
	"status --url <dsn> | apply --url <dsn> | contract --url <dsn> | drift --url <dsn>"

// migrateCommand runs one migrate subcommand.
func migrateCommand(args []string, stdout, stderr io.Writer) int {
	parsed, ok := migrateFlags(args, stderr)
	if !ok {
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	space, err := openWorkspace(parsed.config, stdout)
	if err == nil {
		err = space.run(ctx, parsed)
	}

	if err != nil {
		fmt.Fprintf(stderr, "ratel migrate %s: %v\n", parsed.command, err)

		return exitDiagnostics
	}

	return exitOK
}

func migrateFlags(args []string, stderr io.Writer) (arguments, bool) {
	if len(args) == 0 {
		fmt.Fprintln(stderr, migrateUsage)

		return arguments{}, false
	}

	flags := flag.NewFlagSet("migrate "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "ratel.yaml", "configuration file")
	url := flags.String("url", "", "target database")

	if err := flags.Parse(args[1:]); err != nil {
		return arguments{}, false
	}

	parsed := arguments{command: args[0], config: *configPath, url: *url, operands: flags.Args()}
	needsURL, known := map[string]bool{
		"status": true, "apply": true, "contract": true, "drift": true, planCommand: false, lintCommand: false,
		"verify": false,
	}[parsed.command]

	switch {
	case !known,
		parsed.command == planCommand && len(parsed.operands) != 1,
		parsed.command != planCommand && parsed.command != lintCommand && len(parsed.operands) != 0,
		needsURL != (parsed.url != ""):
		fmt.Fprintln(stderr, migrateUsage)

		return arguments{}, false
	default:
		return parsed, true
	}
}

func openWorkspace(configPath string, stdout io.Writer) (*workspace, error) {
	settings, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	parser, err := parse.New(settings.Parser)
	if err != nil {
		return nil, err
	}

	root, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return nil, fmt.Errorf("configuration directory: %w", err)
	}

	files, err := schemaFiles(root, settings.Schema)
	if err != nil {
		return nil, err
	}

	snapshot, err := builtins.For(settings.Postgres.Version)
	if err != nil {
		return nil, err
	}

	built, err := catalog.Build(parser, files, settings.Postgres.Version, snapshot)
	if err != nil {
		return nil, err
	}

	directory := settings.Migrations
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}

	if err := os.MkdirAll(directory, directoryMode); err != nil {
		return nil, fmt.Errorf("migrations directory: %w", err)
	}

	sources, err := migrate.Read(os.DirFS(directory))
	if err != nil {
		return nil, err
	}

	return &workspace{
		settings: settings, parser: parser, schema: files, catalog: built, directory: directory, sources: sources,
		quoter: ident.New(parser), stdout: stdout,
	}, nil
}

func (w *workspace) run(ctx context.Context, parsed arguments) error {
	switch parsed.command {
	case "verify":
		return w.verify()
	case planCommand:
		return w.plan(ctx, parsed.operands[0])
	case lintCommand:
		return w.lint(ctx, parsed.operands)
	case "status":
		return w.status(ctx, parsed.url)
	case "apply":
		return w.apply(ctx, parsed.url, plan.Expand, plan.Backfill)
	case "contract":
		return w.apply(ctx, parsed.url, plan.Contract)
	case "drift":
		return w.drift(ctx, parsed.url)
	default:
		return fmt.Errorf("%w %q", errUnknownCommand, parsed.command)
	}
}

// verify checks the migration files against ratel.sum; a directory without
// files needs none.
func (w *workspace) verify() error {
	recorded, err := os.ReadFile(filepath.Join(w.directory, migrate.SumFile))
	if errors.Is(err, os.ErrNotExist) && len(w.sources) == 0 {
		return nil
	}

	if err != nil {
		return fmt.Errorf("%s: %w", migrate.SumFile, err)
	}

	return migrate.Verify(w.sources, recorded)
}

// files parses and classifies the migration files.
func (w *workspace) files() ([]migrate.File, error) {
	classifier := safety.NewClassifier(w.parser, safety.Env{})
	files := make([]migrate.File, 0, len(w.sources))

	for _, source := range w.sources {
		file, err := migrate.Parse(source.Name, source.SQL)
		if err != nil {
			return nil, err
		}

		migrate.Classify(&file, classifier, w.parser)
		files = append(files, file)
	}

	return files, nil
}
