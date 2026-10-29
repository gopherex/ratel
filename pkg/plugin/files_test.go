package plugin_test

import (
	"errors"
	"testing"

	"github.com/gopherex/ratel/pkg/plugin"
	"github.com/gopherex/ratel/pkg/plugin/pluginpb"
)

func TestValidateFiles(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		paths []string
		fail  bool
	}{
		{"empty", nil, false},
		{"nested", []string{"models/user.go", "models/order.go"}, false},
		{"absolute", []string{"/etc/passwd"}, true},
		{"parent", []string{"../escape"}, true},
		{"clean parent", []string{"a/../b"}, true},
		{"windows", []string{"C:/escape"}, true},
		{"backslash", []string{"a\\b"}, true},
		{"dot", []string{"."}, true},
		{"duplicate", []string{"a", "a"}, true},
		{"directory collision", []string{"a", "a/b"}, true},
		{"reverse directory collision", []string{"a/b", "a"}, true},
		{"empty path", []string{""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			files := make([]*pluginpb.File, 0, len(tc.paths))
			for _, name := range tc.paths {
				files = append(files, &pluginpb.File{Path: name})
			}

			err := plugin.ValidateFiles(files)
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}

			var failure *plugin.Error
			if tc.fail && (!errors.As(err, &failure) || failure.Code != "RTL-H04") {
				t.Fatalf("error = %v, want RTL-H04", err)
			}
		})
	}
}

func BenchmarkValidateFiles(b *testing.B) {
	files := []*pluginpb.File{{Path: "models/user.go"}, {Path: "models/order.go"}}
	for b.Loop() {
		if err := plugin.ValidateFiles(files); err != nil {
			b.Fatal(err)
		}
	}
}
