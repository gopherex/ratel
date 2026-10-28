package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/cli"
	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

func TestInit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, goMod, plugin string
	}{
		{name: "module", goMod: "module github.com/acme/app\n\ngo 1.27\n", plugin: "github.com/acme/app/internal/db"},
		{name: "no module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if tc.goMod != "" {
				writeFile(t, filepath.Join(root, "go.mod"), tc.goMod)
			}

			var stdout, stderr bytes.Buffer
			if code := cli.Main([]string{"ratel", "init", "--dir", root}, &stdout, &stderr); code != 0 {
				t.Fatalf("init = %d: %s", code, stderr.String())
			}

			settings, err := config.Load(filepath.Join(root, "ratel.yaml"))
			if err != nil {
				t.Fatal(err)
			}

			for _, sub := range []string{"schema/schema.sql", "queries", "migrations"} {
				if _, err := os.Stat(filepath.Join(root, sub)); err != nil {
					t.Errorf("%s: %v", sub, err)
				}
			}

			switch {
			case tc.plugin == "" && len(settings.Plugins) != 0,
				tc.plugin != "" && (len(settings.Plugins) != 1 || settings.Plugins[0].Options["import_path"] != tc.plugin):
				t.Errorf("plugins = %+v", settings.Plugins)
			}

			// The fresh project checks cleanly.
			if code := cli.Main([]string{"ratel", "check", "--config", filepath.Join(root, "ratel.yaml")},
				&stdout, &stderr); code != 0 {
				t.Fatalf("check = %d: %s", code, stderr.String())
			}

			if code := cli.Main([]string{"ratel", "init", "--dir", root}, &stdout, &stderr); code != 1 ||
				!strings.Contains(stderr.String(), "ratel.yaml exists") {
				t.Errorf("second init = %d: %s", code, stderr.String())
			}
		})
	}
}

func TestInitUsage(t *testing.T) {
	t.Parallel()

	if code := cli.Main([]string{"ratel", "init", "extra"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("init with an operand = %d", code)
	}
}
