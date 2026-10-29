// Package plugin hosts built-in, executable and WASI output plugins.
package plugin

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/tetratelabs/wazero"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"
	sdk "github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

// Host owns a run-local Info cache. Construct a new host for each CLI run.
type Host struct {
	builtins map[string]sdk.Generator
	mu       sync.Mutex
	infos    map[string]*pluginpb.InfoResponse
	cache    wazero.CompilationCache
}

// New registers the in-process implementations without invoking them.
func New(builtins map[string]sdk.Generator) *Host {
	return &Host{
		builtins: maps.Clone(builtins), infos: make(map[string]*pluginpb.InfoResponse), cache: wazero.NewCompilationCache(),
	}
}

// Close releases compiled WASI code. Call after all host operations complete.
func (h *Host) Close(ctx context.Context) error {
	if err := h.cache.Close(ctx); err != nil {
		return fmt.Errorf("close plugin compilation cache: %w", err)
	}

	return nil
}

// Prepare negotiates and returns the exact replayable Generate envelope.
// It leaves the caller's input unchanged and does not invoke Generate.
func (h *Host) Prepare(
	ctx context.Context, entry config.Plugin, input *pluginpb.GenerateRequest,
) (*pluginpb.PluginRequest, error) {
	if err := config.ValidatePlugin(entry); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, entry.Timeout)
	defer cancel()

	target, err := h.resolve(ctx, entry)
	if err != nil {
		return nil, err
	}

	return h.prepare(ctx, entry, target, input)
}

func (h *Host) prepare(
	ctx context.Context, entry config.Plugin, target artifact, input *pluginpb.GenerateRequest,
) (*pluginpb.PluginRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("plugin canceled: %w", err)
	}

	info, err := h.info(ctx, entry, target)
	if err != nil {
		return nil, err
	}

	version, err := sdk.Negotiate(sdk.MinVersion, sdk.MaxVersion,
		info.GetMinProtocolVersion(), info.GetMaxProtocolVersion())
	if err != nil {
		return nil, err
	}

	payload := new(pluginpb.GenerateRequest)
	proto.Merge(payload, input)
	payload.ProtocolVersion = version
	payload.OutDir = entry.Out

	return &pluginpb.PluginRequest{
		ProtocolVersion: version, Request: &pluginpb.PluginRequest_Generate{Generate: payload},
	}, nil
}

// Run negotiates, invokes Generate and validates the returned file set.
// The caller owns persistence; this host never writes plugin output to disk.
func (h *Host) Run(
	ctx context.Context, entry config.Plugin, input *pluginpb.GenerateRequest,
) (*pluginpb.PluginResponse, error) {
	if err := config.ValidatePlugin(entry); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, entry.Timeout)
	defer cancel()

	target, err := h.resolve(ctx, entry)
	if err != nil {
		return nil, err
	}

	request, err := h.prepare(ctx, entry, target, input)
	if err != nil {
		return nil, err
	}

	response, err := h.exchange(ctx, entry, target, request)
	if err != nil {
		return nil, err
	}

	if err := ValidateResponse(response); err != nil {
		return response, err
	}

	return response, nil
}

func (h *Host) info(ctx context.Context, entry config.Plugin, target artifact) (*pluginpb.InfoResponse, error) {
	h.mu.Lock()
	cached := h.infos[target.key]
	h.mu.Unlock()

	if cached != nil {
		return cached, nil
	}

	request := &pluginpb.PluginRequest{Request: &pluginpb.PluginRequest_Info{Info: &pluginpb.InfoRequest{
		MinProtocolVersion: sdk.MinVersion, MaxProtocolVersion: sdk.MaxVersion,
	}}}

	response, err := h.exchange(ctx, entry, target, request)
	if err != nil {
		return nil, err
	}

	if err := diagnostics(response); err != nil {
		return nil, err
	}

	info := response.GetInfo()
	if err := sdk.ValidateInfo(info); err != nil {
		return nil, err
	}

	if info.GetName() != entry.Name {
		return nil, &sdk.Error{Code: sdk.InvalidMessage, Message: "configured plugin name differs from Info name"}
	}

	h.mu.Lock()
	h.infos[target.key] = info
	h.mu.Unlock()

	return info, nil
}

func diagnostics(response *pluginpb.PluginResponse) error {
	for _, diagnostic := range response.GetDiagnostics() {
		if diagnostic.GetSeverity() == ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR {
			return &sdk.Error{
				Code: diagnostic.GetCode(), Message: diagnostic.GetMessage(), Source: diagnostic.GetSource(),
			}
		}
	}

	return nil
}

// ValidateResponse enforces errors-as-data, relative safe paths and uniqueness.
func ValidateResponse(response *pluginpb.PluginResponse) error {
	if err := diagnostics(response); err != nil {
		return err
	}

	if response.GetGenerate() == nil {
		return &sdk.Error{Code: sdk.InvalidMessage, Message: "expected Generate response"}
	}

	return sdk.ValidateFiles(response.GetGenerate().GetFiles())
}
