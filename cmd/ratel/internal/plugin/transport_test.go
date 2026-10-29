package plugin_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

func TestTransportFailures(t *testing.T) {
	t.Parallel()

	artifact := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(artifact, []byte("\x00asm"), 0o600); err != nil {
		t.Fatal(err)
	}

	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)

	for _, tc := range []struct {
		name  string
		entry config.Plugin
		code  string // the stable code, when the failure has one
	}{
		{"missing builtin", config.Plugin{Builtin: true}, "RTL-H05"},
		{"missing executable", config.Plugin{Exec: "ratel-plugin-not-found"}, ""},
		{"missing wasm", config.Plugin{Wasm: "absent.wasm", SHA256: strings.Repeat("0", 64)}, ""},
		{"sha256 mismatch", config.Plugin{Wasm: artifact, SHA256: strings.Repeat("0", 64)}, "RTL-H06"},
		{"download failure", config.Plugin{Wasm: missing.URL + "/plugin.wasm", SHA256: strings.Repeat("0", 64)}, "RTL-H07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entry := tc.entry
			entry.Name = "fixture"
			entry.Out = "gen"
			entry.Timeout = time.Second

			_, err := newHost(t, nil).Run(t.Context(), entry, new(pluginpb.GenerateRequest))
			if err == nil {
				t.Fatal("expected failure")
			}

			var failure *sdk.Error
			if tc.code != "" && (!errors.As(err, &failure) || failure.Code != tc.code) {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}
