package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	sdk "github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

type artifact struct {
	path, key string
	wasm      []byte
}

func (h *Host) resolve(ctx context.Context, entry config.Plugin) (artifact, error) {
	if entry.Builtin {
		if h.builtins[entry.Name] == nil {
			return artifact{}, &sdk.Error{Code: "RTL-P05", Message: "unknown builtin " + entry.Name}
		}

		return artifact{key: "builtin:" + entry.Name}, nil
	}

	location := entry.Wasm

	if entry.Exec != "" {
		resolved, err := exec.LookPath(entry.Exec)
		if err != nil {
			return artifact{}, fmt.Errorf("find plugin: %w", err)
		}

		location = resolved
	}

	data, err := readArtifact(ctx, location)
	if err != nil {
		return artifact{}, err
	}

	sum := sha256.Sum256(data)

	digest := hex.EncodeToString(sum[:])
	if entry.SHA256 != "" && !strings.EqualFold(entry.SHA256, digest) {
		return artifact{}, &sdk.Error{Code: "RTL-P06", Message: "plugin sha256 mismatch"}
	}

	result := artifact{path: location, key: entry.Name + ":" + digest}
	if entry.Wasm != "" {
		result.wasm = data
	}

	return result, nil
}

func readArtifact(ctx context.Context, location string) ([]byte, error) {
	if !strings.HasPrefix(location, "https://") && !strings.HasPrefix(location, "http://") {
		data, err := os.ReadFile(location)
		if err != nil {
			return nil, fmt.Errorf("read plugin: %w", err)
		}

		return data, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("plugin URL: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download plugin: %w", err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, &sdk.Error{Code: "RTL-P07", Message: "plugin download: " + response.Status}
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read plugin download: %w", err)
	}

	return data, nil
}

func (h *Host) exchange(
	ctx context.Context, entry config.Plugin, target artifact, request *pluginpb.PluginRequest,
) (*pluginpb.PluginResponse, error) {
	if entry.Builtin {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plugin canceled: %w", err)
		}

		return sdk.Dispatch(ctx, h.builtins[entry.Name], request), nil
	}

	var stdin, stdout, stderr bytes.Buffer
	if err := sdk.Write(&stdin, sdk.Binary, request); err != nil {
		return nil, err
	}

	var err error
	if entry.Wasm != "" {
		err = h.runWasm(ctx, target.wasm, &stdin, &stdout, &stderr)
	} else {
		err = runExec(ctx, target.path, entry.Timeout, &stdin, &stdout, &stderr)
	}

	if err != nil {
		cause := errors.Join(err, ctx.Err())

		return nil, fmt.Errorf("plugin %s: %w; stderr: %s", entry.Name, cause, stderr.String())
	}

	response := new(pluginpb.PluginResponse)
	if err := sdk.Read(&stdout, sdk.Binary, response); err != nil {
		return nil, fmt.Errorf("plugin %s response: %w; stderr: %s", entry.Name, err, stderr.String())
	}

	if !sdk.HasErrors(response) {
		if request.GetInfo() != nil {
			err = sdk.ValidateInfo(response.GetInfo())
		} else {
			err = ValidateResponse(response)
		}

		if err != nil {
			return nil, fmt.Errorf("plugin %s response: %w; stderr: %s", entry.Name, err, stderr.String())
		}
	}

	return response, nil
}

func runExec(
	ctx context.Context, binary string, timeout time.Duration, stdin io.Reader, stdout, stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, binary)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = timeout

	if err := command.Run(); err != nil {
		return fmt.Errorf("run executable: %w", err)
	}

	return nil
}

func (h *Host) runWasm(ctx context.Context, data []byte, stdin io.Reader, stdout, stderr io.Writer) error {
	runtimeConfig := wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithCompilationCache(h.cache)

	runtime := wazero.NewRuntimeWithConfig(ctx, runtimeConfig)
	defer runtime.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return fmt.Errorf("instantiate WASI: %w", err)
	}

	// Explicit compilation keeps shared code alive until Host.Close; implicit
	// InstantiateWithConfig closes its compiled code with each WASI instance.
	compiled, err := runtime.CompileModule(ctx, data)
	if err != nil {
		return fmt.Errorf("compile WASI plugin: %w", err)
	}

	configuration := wazero.NewModuleConfig().WithStdin(stdin).WithStdout(stdout).WithStderr(stderr).WithArgs("plugin")
	if _, err := runtime.InstantiateModule(ctx, compiled, configuration); err != nil {
		return fmt.Errorf("run WASI plugin: %w", err)
	}

	return nil
}
