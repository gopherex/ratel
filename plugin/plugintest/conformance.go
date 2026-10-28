// Package plugintest supplies transport-independent conformance tests for plugin authors.
package plugintest

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
)

// Exchange invokes a fresh plugin exchange in the requested wire format.
// Implementations must honor cancellation and support concurrent calls.
type Exchange func(context.Context, plugin.Format, *pluginpb.PluginRequest) (*pluginpb.PluginResponse, error)

// Case adds an author-owned golden request and expected response to the corpus.
// Version fields are filled after negotiation; diagnostics are compared as data.
type Case struct {
	Name    string
	Request *pluginpb.GenerateRequest
	Want    *pluginpb.PluginResponse
}

// Run checks negotiation, error reporting, file safety, determinism and JSON
// equivalence. The fixed corpus includes an empty catalog and a schema. Additional
// cases assert author-supplied golden responses, including domain refusals.
func Run(t *testing.T, exchange Exchange, cases ...Case) {
	t.Helper()

	RunWithOptions(t, exchange, nil, cases...)
}

// RunWithOptions is Run for a plugin that requires options: the fixed corpus
// requests carry them, encoded as the host encodes ratel.yaml plugin options
// (JSON bytes). Author cases keep their own options.
func RunWithOptions(t *testing.T, exchange Exchange, options []byte, cases ...Case) {
	t.Helper()

	for _, format := range []plugin.Format{plugin.Binary, plugin.JSON} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			version := negotiate(t, exchange, format)

			corpus := append([]Case{
				{Name: "empty", Request: &pluginpb.GenerateRequest{Catalog: &ir.Catalog{}, Options: options}},
				{Name: "schema", Request: &pluginpb.GenerateRequest{Options: options, Catalog: &ir.Catalog{
					Schemas: []*ir.Schema{{Name: "public"}},
				}}},
			}, cases...)
			for _, item := range corpus {
				t.Run(item.Name, func(t *testing.T) {
					t.Parallel()

					checkCase(t, exchange, format, version, item)
				})
			}
		})
	}
}

func negotiate(t *testing.T, exchange Exchange, format plugin.Format) uint32 {
	t.Helper()

	request := &pluginpb.PluginRequest{Request: &pluginpb.PluginRequest_Info{Info: &pluginpb.InfoRequest{
		MinProtocolVersion: plugin.MinVersion, MaxProtocolVersion: plugin.MaxVersion,
	}}}

	response := invoke(t, exchange, format, request)
	if plugin.HasErrors(response) {
		t.Fatalf("Info diagnostics: %v", response.GetDiagnostics())
	}

	info := response.GetInfo()
	if err := plugin.ValidateInfo(info); err != nil {
		t.Fatal(err)
	}

	version, err := plugin.Negotiate(plugin.MinVersion, plugin.MaxVersion,
		info.GetMinProtocolVersion(), info.GetMaxProtocolVersion())
	if err != nil {
		t.Fatal(err)
	}

	for _, invalid := range []*pluginpb.PluginRequest{
		{}, {Request: &pluginpb.PluginRequest_Info{Info: &pluginpb.InfoRequest{}}},
	} {
		if !plugin.HasErrors(invoke(t, exchange, format, invalid)) {
			t.Fatal("invalid request must return ERROR diagnostics")
		}
	}

	return version
}

func invoke(
	t *testing.T, exchange Exchange, format plugin.Format, request *pluginpb.PluginRequest,
) *pluginpb.PluginResponse {
	t.Helper()

	response, err := exchange(t.Context(), format, request)
	if err != nil {
		t.Fatal(err)
	}

	if response == nil {
		t.Fatal("nil plugin response")
	}

	for _, diagnostic := range response.GetDiagnostics() {
		if diagnostic.GetCode() == "" || diagnostic.GetMessage() == "" {
			t.Fatal("diagnostic requires stable code and message")
		}
	}

	return response
}

func checkCase(t *testing.T, exchange Exchange, format plugin.Format, version uint32, item Case) {
	t.Helper()

	payload := new(pluginpb.GenerateRequest)
	proto.Merge(payload, item.Request)
	payload.ProtocolVersion = version
	request := &pluginpb.PluginRequest{
		ProtocolVersion: version, Request: &pluginpb.PluginRequest_Generate{Generate: payload},
	}
	first := invoke(t, exchange, format, request)
	second := invoke(t, exchange, format, request)

	if !proto.Equal(first, second) {
		t.Fatal("non-deterministic response")
	}

	other := plugin.JSON
	if format == plugin.JSON {
		other = plugin.Binary
	}

	if !proto.Equal(first, invoke(t, exchange, other, request)) {
		t.Fatal("binary and JSON responses differ")
	}

	if item.Want != nil && !proto.Equal(first, item.Want) {
		t.Fatalf("golden mismatch: got %v; want %v", first, item.Want)
	}

	if plugin.HasErrors(first) {
		if item.Want == nil {
			t.Fatalf("unexpected ERROR diagnostics: %v", first.GetDiagnostics())
		}

		return
	}

	if first.GetGenerate() == nil {
		t.Fatal("missing Generate response")
	}

	if err := plugin.ValidateFiles(first.GetGenerate().GetFiles()); err != nil {
		t.Fatal(err)
	}
}
