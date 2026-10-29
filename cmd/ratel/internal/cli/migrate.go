package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	tclog "github.com/testcontainers/testcontainers-go/log"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
	"github.com/gopherex/ratel/cmd/ratel/internal/ident"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Migrate errors.
var (
	// errFindings is a plan or lint run with findings that fail it.
	errFindings = errors.New("findings fail the migration")
	// errUnknownCommand is a subcommand migrateFlags did not accept.
	errUnknownCommand = errors.New("unknown subcommand")
)

// Subcommands named more than once: those taking operands, and those using
// the dev server.
const (
	planCommand  = "plan"
	newCommand   = "new"
	lintCommand  = "lint"
	driftCommand = "drift"
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
	log       *slog.Logger // progress and diagnostics, on stderr
}

// arguments are a migrate subcommand's flags and operands.
type arguments struct {
	command, config, url string
	level                slog.Level
	operands             []string
}

// migrateUsage names the subcommands.
const migrateUsage = "usage: ratel migrate plan <name> | new <name> | hash | lint [<file>...] | verify | " +
	"status --url <dsn> | apply --url <dsn> | contract --url <dsn> | drift --url <dsn> " +
	"[--log-level debug|info|warn|error]"

// migrateCommand runs one migrate subcommand.
func migrateCommand(args []string, stdout, stderr io.Writer) int {
	parsed, ok := migrateFlags(args, stderr)
	if !ok {
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log := newLogger(stderr, parsed.level)

	if parsed.command == planCommand || parsed.command == lintCommand || parsed.command == driftCommand {
		// testcontainers prints its Docker banner and some container
		// messages through its process-wide default logger; this process
		// runs one command, so the command sets it.
		tclog.SetDefault(devdb.DebugLogger(log))
	}

	space, err := openWorkspace(parsed.config, stdout, log)
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

	level := slog.LevelInfo
	flags.TextVar(&level, "log-level", slog.LevelInfo, "debug, info, warn or error")

	operands, err := parseInterspersed(flags, args[1:])
	if err != nil {
		return arguments{}, false
	}

	parsed := arguments{command: args[0], config: *configPath, url: *url, level: level, operands: operands}
	needsURL, known := map[string]bool{
		"status": true, "apply": true, "contract": true, driftCommand: true, planCommand: false, newCommand: false,
		"hash": false, lintCommand: false, "verify": false,
	}[parsed.command]
	named := parsed.command == planCommand || parsed.command == newCommand

	switch {
	case !known,
		named && len(parsed.operands) != 1,
		!named && parsed.command != lintCommand && len(parsed.operands) != 0,
		needsURL != (parsed.url != ""):
		fmt.Fprintln(stderr, migrateUsage)

		return arguments{}, false
	default:
		return parsed, true
	}
}

// parseInterspersed parses flags placed before, between or after the
// operands, as in "plan init --log-level debug"; the flag package stops at
// the first operand.
func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	var operands []string

	for {
		if err := flags.Parse(args); err != nil {
			return nil, fmt.Errorf("flags: %w", err)
		}

		if flags.NArg() == 0 {
			return operands, nil
		}

		operands = append(operands, flags.Arg(0))
		args = flags.Args()[1:]
	}
}

// newLogger writes records at level and above to out, one line each,
// without time.
func newLogger(out io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attribute slog.Attr) slog.Attr {
			if len(groups) == 0 && attribute.Key == slog.TimeKey {
				return slog.Attr{}
			}

			return attribute
		},
	}))
}

func openWorkspace(configPath string, stdout io.Writer, log *slog.Logger) (*workspace, error) {
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
		quoter: ident.New(parser), stdout: stdout, log: log,
	}, nil
}

func (w *workspace) run(ctx context.Context, parsed arguments) error {
	switch parsed.command {
	case "verify":
		return w.verify()
	case planCommand:
		return w.plan(ctx, parsed.operands[0])
	case newCommand:
		return w.create(parsed.operands[0])
	case "hash":
		return w.hash()
	case lintCommand:
		return w.lint(ctx, parsed.operands)
	case "status":
		return w.status(ctx, parsed.url)
	case "apply":
		return w.apply(ctx, parsed.url, migrate.Apply)
	case "contract":
		return w.apply(ctx, parsed.url, migrate.Contract)
	case driftCommand:
		return w.drift(ctx, parsed.url)
	default:
		return fmt.Errorf("%w %q", errUnknownCommand, parsed.command)
	}
}

// verify checks what apply checks: the migration files against ratel.sum
// and every step sealed. A directory without files needs no ratel.sum.
func (w *workspace) verify() error {
	_, err := os.Stat(filepath.Join(w.directory, migrate.SumFile))
	if errors.Is(err, os.ErrNotExist) && len(w.sources) == 0 {
		return nil
	}

	return migrate.Check(os.DirFS(w.directory))
}

// files parses and classifies the migration files.
func (w *workspace) files() ([]authoring.File, error) {
	classifier := safety.NewClassifier(w.parser, safety.Env{})
	files := make([]authoring.File, 0, len(w.sources))

	for _, source := range w.sources {
		file, err := authoring.Parse(source)
		if err != nil {
			return nil, err
		}

		authoring.Classify(&file, classifier, w.parser)
		files = append(files, file)
	}

	return files, nil
}
