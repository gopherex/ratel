package plugin_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	host "github.com/gopherex/ratel/cmd/ratel/internal/plugin"
)

func newHost(tb testing.TB, builtins map[string]plugin.Generator) *host.Host {
	tb.Helper()

	runner := host.New(builtins)

	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), time.Second)
		defer cancel()

		if err := runner.Close(ctx); err != nil {
			tb.Error(err)
		}
	})

	return runner
}

func TestClose(t *testing.T) {
	t.Parallel()

	runner := newHost(t, nil)
	if err := runner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type generator struct{ path string }

func (generator) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return &pluginpb.InfoResponse{
		Name: "fixture", AnnotationNamespace: "fixture", MinProtocolVersion: 1, MaxProtocolVersion: 1,
	}, nil
}

func (g generator) Generate(context.Context, *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error) {
	return &pluginpb.GenerateResponse{Files: []*pluginpb.File{{Path: g.path}}}, nil
}

func TestRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path string
		fail bool
	}{{"model.go", false}, {"../escape", true}, {"/absolute", true}, {"a/../b", true}, {"a\\b", true}, {"", true}} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			runner := newHost(t, map[string]plugin.Generator{"fixture": generator{path: tc.path}})

			entry := config.Plugin{Name: "fixture", Builtin: true, Out: "gen", Timeout: time.Second}

			response, err := runner.Run(t.Context(), entry, new(pluginpb.GenerateRequest))
			if (err != nil) != tc.fail {
				t.Fatalf("%v %v", response, err)
			}
		})
	}
}

func TestValidateResponse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		response *pluginpb.PluginResponse
	}{
		{"missing", new(pluginpb.PluginResponse)},
		{"diagnostic", plugin.Failure(&plugin.Error{Code: "X", Message: "invalid"})},
		{"duplicate", &pluginpb.PluginResponse{Response: &pluginpb.PluginResponse_Generate{
			Generate: &pluginpb.GenerateResponse{Files: []*pluginpb.File{{Path: "x"}, {Path: "x"}}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := host.ValidateResponse(tc.response); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

type countedGenerator struct {
	calls atomic.Int32
	info  *pluginpb.InfoResponse
}

func (g *countedGenerator) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	g.calls.Add(1)

	return g.info, nil
}

func (g *countedGenerator) Generate(context.Context, *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error) {
	return &pluginpb.GenerateResponse{}, nil
}

func TestPrepare(t *testing.T) {
	t.Parallel()

	implementation := &countedGenerator{info: &pluginpb.InfoResponse{
		Name: "fixture", AnnotationNamespace: "fixture", MinProtocolVersion: 1, MaxProtocolVersion: 2,
	}}
	runner := newHost(t, map[string]plugin.Generator{"fixture": implementation})
	entry := config.Plugin{Name: "fixture", Builtin: true, Out: "gen", Timeout: time.Second}
	input := &pluginpb.GenerateRequest{Options: []byte("options")}

	for range 2 {
		request, err := runner.Prepare(t.Context(), entry, input)
		if err != nil {
			t.Fatal(err)
		}

		if request.GetProtocolVersion() != 1 || request.GetGenerate().GetOutDir() != "gen" {
			t.Fatal(request)
		}

		request.GetGenerate().Options[0] = 'x'
	}

	if implementation.calls.Load() != 1 {
		t.Fatal("Info cache missed")
	}

	if input.GetProtocolVersion() != 0 || input.GetOutDir() != "" || string(input.GetOptions()) != "options" {
		t.Fatal("Prepare mutated caller input")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := runner.Prepare(ctx, entry, input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNegotiationFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		info *pluginpb.InfoResponse
	}{
		{"missing", nil},
		{"namespace", &pluginpb.InfoResponse{
			Name: "fixture", AnnotationNamespace: "other", MinProtocolVersion: 1, MaxProtocolVersion: 1,
		}},
		{"identity", &pluginpb.InfoResponse{
			Name: "other", AnnotationNamespace: "other", MinProtocolVersion: 1, MaxProtocolVersion: 1,
		}},
		{"range", &pluginpb.InfoResponse{
			Name: "fixture", AnnotationNamespace: "fixture", MinProtocolVersion: 2, MaxProtocolVersion: 3,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runner := newHost(t, map[string]plugin.Generator{"fixture": &countedGenerator{info: tc.info}})
			entry := config.Plugin{Name: "fixture", Builtin: true, Out: "gen", Timeout: time.Second}

			if _, err := runner.Run(t.Context(), entry, new(pluginpb.GenerateRequest)); err == nil {
				t.Fatal("accepted invalid Info")
			}
		})
	}
}

func BenchmarkRun(b *testing.B) {
	runner := newHost(b, map[string]plugin.Generator{"fixture": generator{path: "x"}})
	entry := config.Plugin{Name: "fixture", Builtin: true, Out: "gen", Timeout: time.Second}

	for b.Loop() {
		if _, err := runner.Run(b.Context(), entry, new(pluginpb.GenerateRequest)); err != nil {
			b.Fatal(err)
		}
	}
}
