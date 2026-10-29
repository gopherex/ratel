// Package plugin implements the output-plugin protocol for Go authors.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"
)

// Format selects the single-message wire encoding.
type Format string

const (
	Binary     Format = "binpb"
	JSON       Format = "json"
	MinVersion uint32 = 1
	MaxVersion uint32 = 1
	// InvalidMessage identifies invalid envelopes, identities and encodings.
	InvalidMessage = "RTL-H01"
)

// Error is a protocol or generator refusal with a stable diagnostic code.
type Error struct {
	Code, Message string
	Source        *ir.SourceSpan
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Generator is implemented by built-in and standalone output plugins alike.
type Generator interface {
	Info(ctx context.Context, request *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error)
	Generate(ctx context.Context, request *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error)
}

// Read consumes the complete stream. Trailing bytes are part of the message.
func Read(reader io.Reader, format Format, message proto.Message) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read plugin message: %w", err)
	}

	switch format {
	case Binary:
		err = proto.Unmarshal(data, message)
	case JSON:
		err = protojson.Unmarshal(data, message)
	default:
		return &Error{Code: InvalidMessage, Message: "unsupported format " + string(format)}
	}

	if err != nil {
		return fmt.Errorf("decode plugin message: %w", err)
	}

	return nil
}

// Write emits exactly one message without framing.
func Write(writer io.Writer, format Format, message proto.Message) error {
	var data []byte

	var err error

	switch format {
	case Binary:
		data, err = (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	case JSON:
		data, err = (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
	default:
		return &Error{Code: InvalidMessage, Message: "unsupported format " + string(format)}
	}

	if err != nil {
		return fmt.Errorf("encode plugin message: %w", err)
	}

	written, err := writer.Write(data)
	if err != nil {
		return fmt.Errorf("write plugin message: %w", err)
	}

	if written != len(data) {
		return fmt.Errorf("write plugin message: %w", io.ErrShortWrite)
	}

	return nil
}

// Negotiate selects the highest version in two valid, nonzero ranges.
func Negotiate(hostMin, hostMax, pluginMin, pluginMax uint32) (uint32, error) {
	if hostMin == 0 || pluginMin == 0 || hostMin > hostMax || pluginMin > pluginMax ||
		max(hostMin, pluginMin) > min(hostMax, pluginMax) {
		return 0, &Error{Code: "RTL-H02", Message: fmt.Sprintf(
			"incompatible protocol ranges host [%d,%d], plugin [%d,%d]", hostMin, hostMax, pluginMin, pluginMax,
		)}
	}

	return min(hostMax, pluginMax), nil
}

// HasErrors reports whether any diagnostic fails the exchange.
func HasErrors(response *pluginpb.PluginResponse) bool {
	for _, diagnostic := range response.GetDiagnostics() {
		if diagnostic.GetSeverity() == ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR {
			return true
		}
	}

	return false
}

// Failure translates an error to data. Typed errors retain their code and span.
func Failure(err error) *pluginpb.PluginResponse {
	diagnostic := &ir.Diagnostic{
		Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR, Code: "RTL-H03", Message: err.Error(),
	}

	var refusal *Error
	if errors.As(err, &refusal) {
		diagnostic.Code = refusal.Code
		diagnostic.Message = refusal.Message
		diagnostic.Source = refusal.Source
	}

	return &pluginpb.PluginResponse{Diagnostics: []*ir.Diagnostic{diagnostic}}
}

// Dispatch validates the envelope before invoking a generator.
func Dispatch(ctx context.Context, generator Generator, request *pluginpb.PluginRequest) *pluginpb.PluginResponse {
	switch payload := request.GetRequest().(type) {
	case *pluginpb.PluginRequest_Info:
		if request.GetProtocolVersion() != 0 || payload.Info == nil {
			return Failure(&Error{Code: InvalidMessage, Message: "Info requires protocol_version zero and an Info payload"})
		}

		info, err := generator.Info(ctx, payload.Info)
		if err != nil {
			return Failure(err)
		}

		if err := ValidateInfo(info); err != nil {
			return Failure(err)
		}

		if _, err := negotiateInfo(payload.Info, info); err != nil {
			return Failure(err)
		}

		return &pluginpb.PluginResponse{Response: &pluginpb.PluginResponse_Info{Info: info}}
	case *pluginpb.PluginRequest_Generate:
		return generate(ctx, generator, request.GetProtocolVersion(), payload.Generate)
	default:
		return Failure(&Error{Code: InvalidMessage, Message: "missing request payload"})
	}
}

func negotiateInfo(request *pluginpb.InfoRequest, info *pluginpb.InfoResponse) (uint32, error) {
	return Negotiate(request.GetMinProtocolVersion(), request.GetMaxProtocolVersion(),
		info.GetMinProtocolVersion(), info.GetMaxProtocolVersion())
}

func generate(
	ctx context.Context, generator Generator, version uint32, payload *pluginpb.GenerateRequest,
) *pluginpb.PluginResponse {
	if payload == nil || payload.GetProtocolVersion() != version {
		return Failure(&Error{Code: InvalidMessage, Message: "Generate protocol versions differ"})
	}

	rangeRequest := &pluginpb.InfoRequest{MinProtocolVersion: version, MaxProtocolVersion: version}

	info, err := generator.Info(ctx, rangeRequest)
	if err != nil {
		return Failure(err)
	}

	if err := ValidateInfo(info); err != nil {
		return Failure(err)
	}

	if _, err := negotiateInfo(rangeRequest, info); err != nil {
		return Failure(err)
	}

	output, err := generator.Generate(ctx, payload)
	if err != nil {
		return Failure(err)
	}

	if output == nil {
		return Failure(&Error{Code: InvalidMessage, Message: "nil Generate response"})
	}

	diagnostics := output.GetDiagnostics()
	for _, diagnostic := range diagnostics {
		if diagnostic.GetSeverity() == ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR {
			return Failure(&Error{Code: InvalidMessage, Message: "Generate returned an error diagnostic; return an error"})
		}
	}

	output.Diagnostics = nil

	return &pluginpb.PluginResponse{
		Response: &pluginpb.PluginResponse_Generate{Generate: output}, Diagnostics: diagnostics,
	}
}

// ValidateInfo checks identity, namespace and the advertised version range.
func ValidateInfo(info *pluginpb.InfoResponse) error {
	if info.GetName() == "" || info.GetAnnotationNamespace() != info.GetName() {
		return &Error{Code: InvalidMessage, Message: "Info name must be nonempty and equal annotation_namespace"}
	}

	_, err := Negotiate(info.GetMinProtocolVersion(), info.GetMaxProtocolVersion(),
		info.GetMinProtocolVersion(), info.GetMaxProtocolVersion())

	return err
}

// Serve handles one EOF-delimited exchange. Protocol and generator failures are
// returned as diagnostics; only transport failures are returned as Go errors.
func Serve(ctx context.Context, reader io.Reader, writer io.Writer, format Format, generator Generator) error {
	request := new(pluginpb.PluginRequest)
	if err := Read(reader, format, request); err != nil {
		return Write(writer, format, Failure(err))
	}

	return Write(writer, format, Dispatch(ctx, generator, request))
}
