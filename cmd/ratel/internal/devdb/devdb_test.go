package devdb_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
)

func TestStartRequiresServer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		settings config.Dev
	}{
		{name: "nothing configured"},
		{name: "unparsable url", settings: config.Dev{URL: "postgres://%zz"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server, err := devdb.Start(t.Context(), tc.settings, slog.New(slog.DiscardHandler))
			if err == nil || server != nil {
				t.Fatalf("Start() = %v, %v", server, err)
			}
		})
	}
}

// TestStartLogsHost logs which dev server is used without its credentials;
// the pool connects lazily, so no server needs to listen.
func TestStartLogsHost(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&out, nil))

	server, err := devdb.Start(t.Context(), config.Dev{URL: "postgres://ann:secret@localhost:1/dev"}, logger)
	if err != nil {
		t.Fatal(err)
	}

	if err := server.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "host=localhost:1") || strings.Contains(out.String(), "secret") {
		t.Fatalf("log = %q", out.String())
	}
}

func TestDebugLogger(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		level slog.Level
		want  string
	}{
		{"debug shows it", slog.LevelDebug, "level=DEBUG msg=\"Creating container x\""},
		{"info hides it", slog.LevelInfo, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			devdb.DebugLogger(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: tc.level}))).
				Printf("Creating container %s", "x")

			if !strings.Contains(out.String(), tc.want) || tc.want == "" && out.Len() != 0 {
				t.Fatalf("log = %q", out.String())
			}
		})
	}
}

func BenchmarkDebugLogger(b *testing.B) {
	logger := devdb.DebugLogger(slog.New(slog.DiscardHandler))

	for b.Loop() {
		logger.Printf("Creating container %s", "x")
	}
}
