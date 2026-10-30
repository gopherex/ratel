package cli_test

import (
	"bytes"
	"runtime/debug"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
)

// Exit codes mirrored from the cli package (docs/cli.md).
const (
	codeOK    = 0
	codeUsage = 2
)

func TestMain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		code    int
		wantOut string
		wantErr string
	}{
		{
			name:    "version prints the module version",
			args:    []string{"ratel", "version"},
			code:    codeOK,
			wantOut: "(devel)",
		},
		{
			name:    "version flag prints the module version",
			args:    []string{"ratel", "--version"},
			code:    codeOK,
			wantOut: "(devel)",
		},
		{
			name:    "no command prints help",
			args:    []string{"ratel"},
			code:    codeOK,
			wantOut: "Available Commands:",
		},
		{
			name:    "help flag prints help",
			args:    []string{"ratel", "--help"},
			code:    codeOK,
			wantOut: "Usage:",
		},
		{
			name:    "subcommand help",
			args:    []string{"ratel", "migrate", "apply", "--help"},
			code:    codeOK,
			wantOut: "--url string",
		},
		{
			name:    "migrate without a subcommand prints help",
			args:    []string{"ratel", "migrate"},
			code:    codeOK,
			wantOut: "Available Commands:",
		},
		{
			name:    "unknown command is a usage error",
			args:    []string{"ratel", "bogus"},
			code:    codeUsage,
			wantErr: `unknown command "bogus"`,
		},
		{
			name:    "unknown flag is a usage error",
			args:    []string{"ratel", "check", "--bogus"},
			code:    codeUsage,
			wantErr: "Run 'ratel check --help' for usage.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			code := cli.Main(tc.args, &stdout, &stderr)

			if code != tc.code {
				t.Errorf("Main(%q) code = %d, want %d", tc.args, code, tc.code)
			}

			if !bytes.Contains(stdout.Bytes(), []byte(tc.wantOut)) {
				t.Errorf("Main(%q) stdout = %q, want it to contain %q", tc.args, stdout.String(), tc.wantOut)
			}

			if !bytes.Contains(stderr.Bytes(), []byte(tc.wantErr)) {
				t.Errorf("Main(%q) stderr = %q, want it to contain %q", tc.args, stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestVersionOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{name: "nil info", info: nil, want: "(devel)"},
		{name: "empty version", info: &debug.BuildInfo{}, want: "(devel)"},
		{
			name: "development build",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want: "(devel)",
		},
		{
			name: "release build",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}},
			want: "v0.1.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := cli.VersionOf(tc.info); got != tc.want {
				t.Errorf("VersionOf(%v) = %q, want %q", tc.info, got, tc.want)
			}
		})
	}
}

func BenchmarkMain(b *testing.B) {
	for b.Loop() {
		var stdout bytes.Buffer

		_ = cli.Main([]string{"ratel", "version"}, &stdout, &bytes.Buffer{})
	}
}
