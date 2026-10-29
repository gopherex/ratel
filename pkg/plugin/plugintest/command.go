package plugintest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"
)

// Command adapts a trusted plugin binary for Run. Each exchange has its own
// process and deadline; stderr accompanies transport failures.
func Command(binary string, timeout time.Duration) Exchange {
	return func(
		ctx context.Context, format plugin.Format, request *pluginpb.PluginRequest,
	) (*pluginpb.PluginResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var input, output, stderr bytes.Buffer
		if err := plugin.Write(&input, format, request); err != nil {
			return nil, err
		}

		command := exec.CommandContext(ctx, binary)
		if format == plugin.JSON {
			command = exec.CommandContext(ctx, binary, "--format=json")
		}

		command.Stdin = &input
		command.Stdout = &output
		command.Stderr = &stderr
		command.WaitDelay = timeout

		if err := command.Run(); err != nil {
			return nil, fmt.Errorf("plugin process: %w; stderr: %s", err, stderr.String())
		}

		response := new(pluginpb.PluginResponse)
		if err := plugin.Read(&output, format, response); err != nil {
			return nil, fmt.Errorf("plugin response: %w; stderr: %s", err, stderr.String())
		}

		return response, nil
	}
}
