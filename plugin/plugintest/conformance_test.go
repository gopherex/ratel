package plugintest_test

import (
	"context"
	"testing"

	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
	"github.com/gopherex/ratel/plugin/plugintest"
)

type generator struct{}

func (generator) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return &pluginpb.InfoResponse{
		Name: "fixture", AnnotationNamespace: "fixture", MinProtocolVersion: 1, MaxProtocolVersion: 1,
	}, nil
}

func (generator) Generate(context.Context, *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error) {
	return &pluginpb.GenerateResponse{}, nil
}

func TestConformance(t *testing.T) {
	t.Parallel()
	plugintest.Run(t, func(
		ctx context.Context, _ plugin.Format, request *pluginpb.PluginRequest,
	) (*pluginpb.PluginResponse, error) {
		return plugin.Dispatch(ctx, generator{}, request), nil
	})
}
