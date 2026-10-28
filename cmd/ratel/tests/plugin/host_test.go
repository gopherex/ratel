package plugin_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopherex/ratel/ir"
	sdk "github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
	"github.com/gopherex/ratel/plugin/plugintest"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	host "github.com/gopherex/ratel/cmd/ratel/internal/plugin"
)

func newHost(tb testing.TB) *host.Host {
	tb.Helper()

	runner := host.New(nil)

	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), time.Second)
		defer cancel()

		if err := runner.Close(ctx); err != nil {
			tb.Error(err)
		}
	})

	return runner
}

func build(tb testing.TB, wasm bool) string {
	tb.Helper()
	output := filepath.Join(tb.TempDir(), "generator")

	command := exec.CommandContext(tb.Context(), "go", "build", "-o", output, "./testdata/generator")
	if wasm {
		command.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	}

	if data, err := command.CombinedOutput(); err != nil {
		tb.Fatalf("build plugin: %v: %s", err, data)
	}

	return output
}

func checksum(tb testing.TB, path string) string {
	tb.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func TestTransports(t *testing.T) {
	native, wasm := build(t, false), build(t, true)
	t.Parallel()

	for _, tc := range []struct {
		name, path string
		wasm       bool
	}{{"exec", native, false}, {"wasm", wasm, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entry := config.Plugin{Name: "fixture", Out: "gen", Timeout: 2 * time.Minute, SHA256: checksum(t, tc.path)}
			if tc.wasm {
				entry.Wasm = tc.path
			} else {
				entry.Exec = tc.path
			}

			testTransport(t, entry)
		})
	}

	t.Run("conformance", func(t *testing.T) {
		t.Parallel()
		plugintest.Run(t, plugintest.Command(native, 30*time.Second), plugintest.Case{
			Name: "refusal", Request: &pluginpb.GenerateRequest{Options: []byte("error")},
			Want: &pluginpb.PluginResponse{Diagnostics: []*ir.Diagnostic{{
				Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR, Code: "FIXTURE-ERROR", Message: "requested refusal",
			}}},
		})
	})
	t.Run("dump replay", func(t *testing.T) { t.Parallel(); testReplay(t, native) })
}

func testTransport(t *testing.T, entry config.Plugin) {
	t.Helper()

	runner := newHost(t)

	for _, mode := range []string{"success", "error", "unsafe", "hang", "recovery"} {
		settings := entry
		if mode == "hang" {
			settings.Timeout = 3 * time.Second
		}

		response, err := runner.Run(t.Context(), settings, &pluginpb.GenerateRequest{Options: []byte(mode)})
		fail := mode == "error" || mode == "unsafe" || mode == "hang"

		if (err != nil) != fail {
			t.Fatalf("%s: %v %v", mode, response, err)
		}

		if mode == "hang" && (!errors.Is(err, context.DeadlineExceeded) ||
			!strings.Contains(err.Error(), "fixture-hang")) {
			t.Fatalf("guest did not reach hang or deadline cause lost: %v", err)
		}

		if mode == "success" && string(response.GetGenerate().GetFiles()[0].GetContents()) != "package model\n" {
			t.Fatal(response)
		}
	}

	entry.SHA256 = strings.Repeat("0", 64)
	if _, err := runner.Run(t.Context(), entry, new(pluginpb.GenerateRequest)); err == nil {
		t.Fatal("accepted checksum mismatch")
	}
}

func testReplay(t *testing.T, native string) {
	t.Helper()
	directory := t.TempDir()
	text := "version: 1\nschema: [schema.sql]\nplugins:\n  - name: fixture\n    exec: " + native +
		"\n    sha256: " + checksum(t, native) + "\n    out: gen\n    options: {package: db}\n" +
		"  - name: other\n    builtin: true\n    out: other\n"

	sql := "-- @fixture.model User\n-- @other.model Foreign\nCREATE TABLE users(\n-- @fixture.type ID\nid bigint);"
	if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte(sql), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(directory, "ratel.yaml")

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	var output, stderr bytes.Buffer
	if code := cli.Main([]string{"ratel", "dump", "--config", path, "--plugin", "fixture"}, &output, &stderr); code != 0 {
		t.Fatalf("%d %s", code, &stderr)
	}

	request := new(pluginpb.PluginRequest)
	if err := sdk.Read(&output, sdk.Binary, request); err != nil {
		t.Fatal(err)
	}

	checkReplay(t, request.GetGenerate())

	response, err := plugintest.Command(native, time.Second)(t.Context(), sdk.Binary, request)
	if err != nil || response.GetGenerate() == nil {
		t.Fatalf("replay: %v %v", response, err)
	}
}

func checkReplay(t *testing.T, payload *pluginpb.GenerateRequest) {
	t.Helper()

	if payload.GetProtocolVersion() != 1 || len(payload.GetAnnotations()) != 2 ||
		string(payload.GetOptions()) != "{\"package\":\"db\"}" || payload.GetOutDir() != "gen" {
		t.Fatal(payload)
	}

	if len(payload.GetCatalog().GetDiagnostics()) != 0 {
		t.Fatal("configured namespaces reported unowned")
	}

	for _, annotation := range payload.GetAnnotations() {
		if annotation.GetNamespace() != "fixture" || annotation.GetSource().GetFile() != "schema.sql" ||
			annotation.GetObject() == nil {
			t.Fatal(annotation)
		}
	}
}

func BenchmarkTransports(b *testing.B) {
	for _, wasm := range []bool{false, true} {
		name := "exec"
		if wasm {
			name = "wasm"
		}

		b.Run(name, func(b *testing.B) {
			binary := build(b, wasm)
			entry := config.Plugin{Name: "fixture", Out: "gen", Timeout: 2 * time.Minute, SHA256: checksum(b, binary)}

			if wasm {
				entry.Wasm = binary
			} else {
				entry.Exec = binary
			}

			runner := newHost(b)
			for b.Loop() {
				if _, err := runner.Run(b.Context(), entry, new(pluginpb.GenerateRequest)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
