package plugin_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopherex/ratel/pkg/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

func TestExecDiagnostics(t *testing.T) {
	binary := build(t, false)
	t.Parallel()

	for _, mode := range []string{"malformed", "nonzero", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			alias := filepath.Join(t.TempDir(), mode)
			if err := os.Link(binary, alias); err != nil {
				t.Fatal(err)
			}

			entry := config.Plugin{Name: "fixture", Exec: alias, Out: "gen", SHA256: checksum(t, binary), Timeout: time.Second}
			if _, err := newHost(t).Run(t.Context(), entry, new(pluginpb.GenerateRequest)); err == nil ||
				!strings.Contains(err.Error(), "fixture-stderr") {
				t.Fatalf("stderr lost: %v", err)
			}
		})
	}
}

func TestWasmURL(t *testing.T) {
	binary := build(t, true)
	t.Parallel()

	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/plugin" {
			http.NotFound(writer, request)

			return
		}

		_, _ = writer.Write(data)
	}))
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		name, path, digest string
		fail               bool
	}{
		{"success", "/plugin", checksum(t, binary), false},
		{"checksum", "/plugin", strings.Repeat("0", 64), true},
		{"not found", "/missing", checksum(t, binary), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entry := config.Plugin{
				Name: "fixture", Wasm: server.URL + tc.path, Out: "gen", SHA256: tc.digest, Timeout: 2 * time.Minute,
			}

			_, err := newHost(t).Run(t.Context(), entry, new(pluginpb.GenerateRequest))
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
		})
	}
}
