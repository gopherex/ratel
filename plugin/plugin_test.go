package plugin_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
)

type generator struct{}

func (generator) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return &pluginpb.InfoResponse{
		Name: "fixture", AnnotationNamespace: "fixture", MinProtocolVersion: 1, MaxProtocolVersion: 2,
	}, nil
}

func (generator) Generate(context.Context, *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error) {
	return &pluginpb.GenerateResponse{Files: []*pluginpb.File{
		{Path: "model.go", Contents: []byte("package model\n")},
	}}, nil
}

// diagnosing returns a response carrying one diagnostic of severity.
type diagnosing struct {
	generator
	severity ir.DiagnosticSeverity
}

func (d diagnosing) Generate(context.Context, *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error) {
	return &pluginpb.GenerateResponse{Diagnostics: []*ir.Diagnostic{{Severity: d.severity, Code: "X-1"}}}, nil
}

func TestDispatchDiagnostics(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		severity ir.DiagnosticSeverity
		failed   bool
	}{
		{name: "warning", severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARNING},
		{name: "error", severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response := plugin.Dispatch(t.Context(), diagnosing{severity: tc.severity}, &pluginpb.PluginRequest{
				ProtocolVersion: 1,
				Request:         &pluginpb.PluginRequest_Generate{Generate: &pluginpb.GenerateRequest{ProtocolVersion: 1}},
			})

			if plugin.HasErrors(response) != tc.failed {
				t.Fatalf("Dispatch() = %v", response)
			}

			if !tc.failed && (len(response.GetDiagnostics()) != 1 || len(response.GetGenerate().GetDiagnostics()) != 0) {
				t.Fatalf("warning not moved to the plugin response: %v", response)
			}
		})
	}
}

func infoRequest() *pluginpb.PluginRequest {
	return &pluginpb.PluginRequest{Request: &pluginpb.PluginRequest_Info{Info: &pluginpb.InfoRequest{
		MinProtocolVersion: 1, MaxProtocolVersion: 1,
	}}}
}

func TestServe(t *testing.T) {
	t.Parallel()

	for _, format := range []plugin.Format{plugin.Binary, plugin.JSON} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			var input, output bytes.Buffer

			if err := plugin.Write(&input, format, infoRequest()); err != nil {
				t.Fatal(err)
			}

			if err := plugin.Serve(t.Context(), &input, &output, format, generator{}); err != nil {
				t.Fatal(err)
			}

			response := new(pluginpb.PluginResponse)
			if err := plugin.Read(&output, format, response); err != nil {
				t.Fatal(err)
			}

			if response.GetInfo().GetName() != "fixture" {
				t.Fatal(response)
			}
		})
	}
}

func TestDispatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		version uint32
		fail    bool
	}{{"supported", 2, false}, {"zero", 0, true}, {"unsupported", 3, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := &pluginpb.PluginRequest{
				ProtocolVersion: tc.version, Request: &pluginpb.PluginRequest_Generate{
					Generate: &pluginpb.GenerateRequest{ProtocolVersion: tc.version},
				},
			}

			response := plugin.Dispatch(t.Context(), generator{}, request)
			if plugin.HasErrors(response) != tc.fail {
				t.Fatal(response)
			}
		})
	}
}

func TestNegotiate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		min, max uint32
		want     uint32
	}{{"overlap", 1, 2, 2}, {"disjoint", 3, 4, 0}, {"invalid", 2, 1, 0}, {"zero", 0, 1, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := plugin.Negotiate(1, 2, tc.min, tc.max)
			if got != tc.want || (err != nil) != (tc.want == 0) {
				t.Fatalf("%d %v", got, err)
			}
		})
	}
}

func TestCodec(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		format plugin.Format
		data   string
	}{
		{"binary", plugin.Binary, "\xff"},
		{"json", plugin.JSON, "{} {}"},
		{"unknown", plugin.Format("xml"), "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := plugin.Read(bytes.NewBufferString(tc.data), tc.format, new(pluginpb.PluginRequest)); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

type brokenStream struct{}

func (brokenStream) Read([]byte) (int, error)  { return 0, io.ErrUnexpectedEOF }
func (brokenStream) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

func TestTransportErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		writer io.Writer
		want   error
	}{
		{"broken", brokenStream{}, io.ErrClosedPipe},
		{"short", shortWriter{}, io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := plugin.Write(tc.writer, plugin.Binary, infoRequest()); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}

	err := plugin.Read(brokenStream{}, plugin.Binary, new(pluginpb.PluginRequest))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

func TestFailure(t *testing.T) {
	t.Parallel()

	source := &ir.SourceSpan{File: "schema.sql", StartLine: 2}
	err := &plugin.Error{Code: "FIXTURE-ERROR", Message: "invalid annotation", Source: source}
	response := plugin.Failure(err)

	if !plugin.HasErrors(response) || response.GetDiagnostics()[0].GetCode() != err.Code ||
		response.GetDiagnostics()[0].GetMessage() != err.Message ||
		!proto.Equal(source, response.GetDiagnostics()[0].GetSource()) {
		t.Fatal(response)
	}
}

func BenchmarkDispatch(b *testing.B) {
	request := infoRequest()
	for b.Loop() {
		plugin.Dispatch(b.Context(), generator{}, request)
	}
}

func BenchmarkCodec(b *testing.B) {
	data, err := proto.Marshal(infoRequest())
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if err := plugin.Read(bytes.NewReader(data), plugin.Binary, new(pluginpb.PluginRequest)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNegotiate(b *testing.B) {
	for b.Loop() {
		if _, err := plugin.Negotiate(1, 2, 1, 2); err != nil {
			b.Fatal(err)
		}
	}
}
