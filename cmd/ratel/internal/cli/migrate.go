package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	tclog "github.com/testcontainers/testcontainers-go/log"

	"github.com/gopherex/ratel/pkg/ir"
	"github.com/gopherex/ratel/pkg/migrate"

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
	// errURL is a target command without --url.
	errURL = errors.New("--url is required")
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

// migrateFlags are the flags every migrate subcommand shares.
type migrateFlags struct {
	config string
	level  slog.Level
}

// open reads the workspace a subcommand runs on.
func (f *migrateFlags) open(command *cobra.Command) (*workspace, error) {
	return openWorkspace(f.config, command.OutOrStdout(), newLogger(command.ErrOrStderr(), f.level))
}

func migrateCommand() *cobra.Command {
	flags := &migrateFlags{level: slog.LevelInfo}
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Plan, check and apply migrations",
		Long: `migrate plans migrations from the schema files, checks them and applies them
without blocking traffic. Results go to stdout, progress to stderr as slog
records at --log-level.`,
		// Without Args and RunE cobra prints help for an unknown subcommand
		// and succeeds; with them it is a usage error.
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error { return command.Help() },
	}
	command.PersistentFlags().StringVar(&flags.config, "config", "ratel.yaml", "configuration file")
	command.PersistentFlags().TextVar(&flags.level, "log-level", slog.LevelInfo, "debug, info, warn or error")

	command.AddCommand(
		flags.local("plan <name>", "Diff the schema against the migrations and write the planned steps",
			cobra.ExactArgs(1), func(ctx context.Context, w *workspace, operands []string) error {
				w.devServer()

				return w.plan(ctx, operands[0])
			}),
		flags.local("new <name>", "Write an empty hand-written migration file", cobra.ExactArgs(1),
			func(_ context.Context, w *workspace, operands []string) error { return w.create(operands[0]) }),
		flags.local("hash", "Seal hand-written steps and update ratel.sum", cobra.NoArgs,
			func(_ context.Context, w *workspace, _ []string) error { return w.hash() }),
		flags.local("lint [<file>...]", "Run the RTL rules over planned and hand-written migrations",
			cobra.ArbitraryArgs, func(ctx context.Context, w *workspace, operands []string) error {
				w.devServer()

				return w.lint(ctx, operands)
			}),
		flags.local("verify", "Check ratel.sum and that every step is sealed", cobra.NoArgs,
			func(_ context.Context, w *workspace, _ []string) error { return w.verify() }),
		flags.target("status", "Print the journal state of every step",
			func(ctx context.Context, w *workspace, url string) error { return w.status(ctx, url) }),
		flags.target("apply", "Run pending expand and backfill steps",
			func(ctx context.Context, w *workspace, url string) error { return w.apply(ctx, url, migrate.Apply) }),
		flags.target("contract", "Run pending contract steps",
			func(ctx context.Context, w *workspace, url string) error { return w.apply(ctx, url, migrate.Contract) }),
		flags.target("drift", "Diff the target against the state its applied migrations produce",
			func(ctx context.Context, w *workspace, url string) error {
				w.devServer()

				return w.drift(ctx, url)
			}),
	)

	return command
}

// local is a migrate subcommand that needs no target database.
func (f *migrateFlags) local(use, short string, args cobra.PositionalArgs,
	run func(context.Context, *workspace, []string) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(command *cobra.Command, operands []string) error {
			space, err := f.open(command)
			if err == nil {
				err = run(command.Context(), space, operands)
			}

			return failure(err)
		},
	}
}

// target is a migrate subcommand run against the database of --url.
func (f *migrateFlags) target(use, short string, run func(context.Context, *workspace, string) error) *cobra.Command {
	var url string

	command := f.local(use, short, cobra.NoArgs, func(ctx context.Context, w *workspace, _ []string) error {
		return run(ctx, w, url)
	})
	command.Flags().StringVar(&url, "url", "", "target database (required)")
	command.PreRunE = func(*cobra.Command, []string) error {
		if url == "" {
			return errURL
		}

		return nil
	}

	return command
}

// devServer routes testcontainers' messages into the command's log: the
// library prints its Docker banner and container messages through a
// process-wide default logger, and this process runs one command.
func (w *workspace) devServer() {
	tclog.SetDefault(devdb.DebugLogger(w.log))
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
