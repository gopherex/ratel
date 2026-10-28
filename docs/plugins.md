# Output plugins

An output plugin turns the IR into files. The Go generator `ratel-gen-go` is
the first one and ships with ratel; plugins for other languages implement the
same protocol. Messages are defined in `proto/ratel/v1/plugin` (package
`ratel.v1.plugin`).

## Transport

- One request on stdin, one response on stdout, each a single protobuf message
  read to end of stream. No length prefix, no tag byte, no gRPC.
- stderr is captured and attached to the failure diagnostic when the plugin
  exits non-zero or writes an invalid response.
- `--format=json` (protojson with proto field names) is accepted by every
  plugin instead of binary, for debugging and for languages without protobuf.
- Each exchange uses a fresh process or WASI instance, because stdin and stdout
  end at EOF. A host run caches `Info` by plugin identity and SHA-256 digest;
  it performs one `Generate` exchange per configured plugin. A cold run therefore
  starts an external plugin twice, not once. Built-ins use the same envelopes
  through `plugin.Generator` without a process.

| Kind | Configured as | Executed by |
|---|---|---|
| `builtin` | `go` | in-process, through the same messages |
| `exec` | path or name on `PATH` | a child process |
| `wasm` | path or URL plus `sha256` | wazero, WASI preview 1, stdin and stdout only |

For `wasm` and for `exec` given by path, `sha256` of the file is mandatory and
verified before every run; a mismatch fails the run.

## Messages

```proto
message PluginRequest {
  uint32 protocol_version = 1;
  oneof request {
    InfoRequest info = 2;
    GenerateRequest generate = 3;
  }
}

message PluginResponse {
  oneof response {
    InfoResponse info = 1;
    GenerateResponse generate = 2;
  }
  repeated ratel.v1.ir.Diagnostic diagnostics = 3;
}
```

- `InfoRequest`: the host's `min_protocol_version` and `max_protocol_version`.
- `InfoResponse`: name, version, the plugin's `min_protocol_version` and
  `max_protocol_version`, its annotation namespace (equal to the plugin name),
  a description of the plugin options.
- `GenerateRequest`: the negotiated `protocol_version`, the `Catalog`, the
  queries, plugin options as bytes (the plugin decodes them), the output
  directory, host version, and the annotations of the plugin's namespace in
  the generic parsed form of [input.md](input.md#syntax) with their source
  spans.
- `GenerateResponse`: files with paths relative to the output directory.

## Rules

1. **Negotiation.** The host and the plugin exchange their ranges in `Info`;
   the protocol version of `Generate` is the highest version in both ranges.
   No overlap fails before `Generate` with a diagnostic naming both ranges.
2. **Output is pure.** A plugin writes nothing to disk itself; it returns files.
   The host writes them, and only inside the output directory: absolute paths
   and `..` are errors.
3. **Owned output.** The host deletes files in the output directory that it
   wrote in the previous run and that are absent now; it never touches other
   files. Ownership is recorded in a manifest file in the output directory.
4. **Deterministic.** The same request yields byte-identical files. *Check:*
   the conformance suite runs each plugin twice and compares.
5. **Errors as data.** A diagnostic with severity `ERROR` fails the run; codes
   are stable strings owned by the plugin.
6. **Own annotations.** The host checks only the generic syntax of plugin
   annotations; the plugin validates their names, targets and values and
   reports problems as diagnostics with the annotation's span.
7. **Replay.** `ratel dump --plugin <name>` writes the exact request to a file,
   so `ratel-gen-x < request.binpb` reproduces a run without the host.

## Go SDK

Package `github.com/gopherex/ratel/plugin` supplies `Read`, `Write`, `Negotiate`,
`Dispatch` and `Serve`. Implement `plugin.Generator` (`Info` and `Generate`),
parse `--format` with a default of `binpb`, then call
`plugin.Serve(ctx, os.Stdin, os.Stdout, format, generator)`. `Serve` handles one
exchange and returns only transport errors. Generator errors become `ERROR`
diagnostics. Return `*plugin.Error` to preserve a stable code and source span;
other errors use `RTL-P03`. Plugins must honor the passed context. `Info` must
be side-effect-free: the SDK also calls it locally to validate Generate's version.

The host package `cmd/ratel/internal/plugin` exposes `Prepare` (negotiate and
construct a replay request) and `Run` (negotiate, generate, validate). It returns
files without persisting them. Call `Host.Close(ctx)` after all operations to
release its run-local compilation cache. WASI instances are fresh per exchange;
compiled code is shared until the host closes. The `generate` command's output
writer owns the manifest and filesystem safety checks, including symlink handling. `Run` rejects
unsafe paths, duplicate files and file/directory collisions before returning.
The host registers one built-in generator, `go` (`cmd/ratel/internal/gengo`,
`ratel-gen-go`).

Exec plugins inherit the host environment and filesystem permissions: they are
trusted executables, not a sandbox. WASI plugins receive no filesystem mounts,
network access or inherited environment. Each plugin has a configured deadline,
including verification, negotiation and generation; WASI execution is interrupted
when the context expires. stderr is retained for failed or malformed exchanges.

## Conformance suite

Package `github.com/gopherex/ratel/plugin/plugintest` runs any plugin against
fixed empty-catalog and schema requests, malformed requests and invalid protocol
ranges. It checks rules 2, 4 and 5, negotiation, repeated-response determinism
and binary/JSON equivalence. Authors add `plugintest.Case` values with their own
requests and golden responses, including expected error diagnostics.

```go
func TestPlugin(t *testing.T) {
    t.Parallel()
    plugintest.Run(t, plugintest.Command("./ratel-gen-x", time.Minute))
}
```

For non-exec transports supply a `plugintest.Exchange` function instead of
`Command`. Exchanges must support concurrent calls and honor cancellation.
`cmd/ratel/tests/plugin` builds a real exec fixture and the same fixture with
`GOOS=wasip1 GOARCH=wasm`, checks host execution, checksums, deadlines and recovery,
and runs the conformance suite against the executable. The future Go generator
uses the same suite.
