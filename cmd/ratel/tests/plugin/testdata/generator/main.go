package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
)

type generator struct{}

func (generator) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return &pluginpb.InfoResponse{Name: "fixture", Version: "1.0.0", AnnotationNamespace: "fixture", MinProtocolVersion: 1, MaxProtocolVersion: 1}, nil
}
func (generator) Generate(_ context.Context, request *pluginpb.GenerateRequest) (*pluginpb.GenerateResponse, error) {
	switch string(request.GetOptions()) {
	case "hang":
		_, _ = io.WriteString(os.Stderr, "fixture-hang")
		for {
		}
	case "error":
		return nil, &plugin.Error{Code: "FIXTURE-ERROR", Message: "requested refusal"}
	case "unsafe":
		return &pluginpb.GenerateResponse{Files: []*pluginpb.File{{Path: "../escape"}}}, nil
	default:
		return &pluginpb.GenerateResponse{Files: []*pluginpb.File{{Path: "model.go", Contents: []byte("package model\n")}}}, nil
	}
}
func main() {
	format := flag.String("format", "binpb", "wire format")
	flag.Parse()
	switch filepath.Base(os.Args[0]) {
	case "malformed":
		_, _ = io.WriteString(os.Stderr, "fixture-stderr")
		_, _ = os.Stdout.Write([]byte{0xff})
		return
	case "nonzero":
		_, _ = io.WriteString(os.Stderr, "fixture-stderr")
		os.Exit(7)
	case "wrong-kind":
		_, _ = io.WriteString(os.Stderr, "fixture-stderr")
		_ = plugin.Write(os.Stdout, plugin.Binary, &pluginpb.PluginResponse{
			Response: &pluginpb.PluginResponse_Generate{Generate: &pluginpb.GenerateResponse{}},
		})
		return
	}
	if err := plugin.Serve(context.Background(), os.Stdin, os.Stdout, plugin.Format(*format), generator{}); err != nil {
		os.Exit(1)
	}
}
