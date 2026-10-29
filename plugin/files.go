package plugin

import (
	"fmt"
	"path"
	"strings"

	"github.com/gopherex/ratel/plugin/pluginpb"
)

// ValidateFiles checks portable relative file paths, duplicates and file/directory
// collisions. It does not access the filesystem or authorize following symlinks.
func ValidateFiles(files []*pluginpb.File) error {
	seen := make(map[string]bool, len(files))

	for _, file := range files {
		name := file.GetPath()
		if name == "" || name == "." || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) ||
			path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." || seen[name] {
			return &Error{Code: "RTL-H04", Message: fmt.Sprintf("unsafe or duplicate output path %q", name)}
		}

		seen[name] = true
	}

	for _, file := range files {
		name := file.GetPath()

		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return &Error{Code: "RTL-H04", Message: fmt.Sprintf("output file %q is also a directory", parent)}
			}
		}
	}

	return nil
}
