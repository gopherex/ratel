package plugin_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gopherex/ratel/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

func TestTransportFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		entry config.Plugin
	}{
		{"missing builtin", config.Plugin{Builtin: true}},
		{"missing executable", config.Plugin{Exec: "ratel-plugin-not-found"}},
		{"missing wasm", config.Plugin{Wasm: "absent.wasm", SHA256: strings.Repeat("0", 64)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entry := tc.entry
			entry.Name = "fixture"
			entry.Out = "gen"
			entry.Timeout = time.Second

			if _, err := newHost(t, nil).Run(t.Context(), entry, new(pluginpb.GenerateRequest)); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}
