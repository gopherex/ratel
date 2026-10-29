package gengo_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"
	"github.com/gopherex/ratel/pkg/plugin/plugintest"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

// TestConformance runs the plugin conformance suite against the built-in
// generator through the same encoded exchange an executable plugin serves.
func TestConformance(t *testing.T) {
	t.Parallel()

	plugintest.RunWithOptions(t, func(ctx context.Context, format plugin.Format, request *pluginpb.PluginRequest) (
		*pluginpb.PluginResponse, error,
	) {
		var input, output bytes.Buffer

		if err := plugin.Write(&input, format, request); err != nil {
			return nil, err
		}

		if err := plugin.Serve(ctx, &input, &output, format, gengo.Generator{}); err != nil {
			return nil, err
		}

		response := new(pluginpb.PluginResponse)

		return response, plugin.Read(&output, format, response)
	}, []byte(options))
}
