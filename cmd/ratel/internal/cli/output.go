package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gopherex/ratel/plugin/pluginpb"
)

// manifest records the files a plugin wrote into its output directory, one
// slash-separated relative path per line, so the next run deletes only files
// it owns.
const manifest = ".ratel-manifest"

const (
	fileMode       = 0o644
	executableMode = 0o755
	directoryMode  = 0o755
)

var (
	// errSymlink refuses output through a symbolic link: it could write or
	// delete outside the output directory.
	errSymlink = errors.New("output path crosses a symbolic link")
	// errOutside refuses a path that leaves the output directory.
	errOutside = errors.New("path is outside the output directory")
)

// writeOutput writes a plugin's files under out, deletes the files the
// previous run wrote that are absent now, and records the new manifest.
// Files the manifest does not list are never touched.
func writeOutput(out string, files []*pluginpb.File) error {
	if err := os.MkdirAll(out, directoryMode); err != nil {
		return fmt.Errorf("output directory: %w", err)
	}

	if err := noSymlink(out, "."); err != nil {
		return err
	}

	previous, err := readManifest(out)
	if err != nil {
		return err
	}

	written := make([]string, 0, len(files))

	for _, file := range files {
		if err := writeFile(out, file); err != nil {
			return err
		}

		written = append(written, file.GetPath())
	}

	for _, stale := range previous {
		if !slices.Contains(written, stale) {
			if err := removeOwned(out, stale); err != nil {
				return err
			}
		}
	}

	slices.Sort(written)

	return atomicWrite(filepath.Join(out, manifest), []byte(strings.Join(written, "\n")+"\n"), fileMode)
}

func readManifest(out string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(out, manifest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifest, err)
	}

	var paths []string

	for line := range strings.SplitSeq(string(data), "\n") {
		if line != "" {
			if !filepath.IsLocal(filepath.FromSlash(line)) {
				return nil, fmt.Errorf("%s lists %s: %w", manifest, line, errOutside)
			}

			paths = append(paths, line)
		}
	}

	return paths, nil
}

// noSymlink checks that no component of out/relative is a symbolic link.
func noSymlink(out, relative string) error {
	current := out

	for part := range strings.SplitSeq(filepath.ToSlash(relative), "/") {
		if part != "." {
			current = filepath.Join(current, part)
		}

		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("output path %s: %w", current, err)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", current, errSymlink)
		}
	}

	return nil
}

func writeFile(out string, file *pluginpb.File) error {
	relative := filepath.FromSlash(file.GetPath())
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("plugin file %q: %w", file.GetPath(), errOutside)
	}

	if err := noSymlink(out, relative); err != nil {
		return err
	}

	target := filepath.Join(out, relative)
	if err := os.MkdirAll(filepath.Dir(target), directoryMode); err != nil {
		return fmt.Errorf("output directory: %w", err)
	}

	mode := fs.FileMode(fileMode)
	if file.GetExecutable() {
		mode = executableMode
	}

	return atomicWrite(target, file.GetContents(), mode)
}

// atomicWrite replaces target through a temporary file in its directory, so
// a reader never sees a partial file.
func atomicWrite(target string, contents []byte, mode fs.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".ratel-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	defer func() { _ = os.Remove(temporary.Name()) }()

	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("write %s: %w", target, err)
	}

	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	if err := os.Chmod(temporary.Name(), mode); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	if err := os.Rename(temporary.Name(), target); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	return nil
}

// removeOwned deletes a file of the previous run, then its directories when
// they became empty, never leaving the output directory.
func removeOwned(out, relative string) error {
	relative = filepath.FromSlash(relative)
	if err := noSymlink(out, relative); err != nil {
		return err
	}

	target := filepath.Join(out, relative)
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale %s: %w", target, err)
	}

	for directory := filepath.Dir(relative); directory != "."; directory = filepath.Dir(directory) {
		entries, err := os.ReadDir(filepath.Join(out, directory))
		if err != nil {
			return fmt.Errorf("read stale directory: %w", err)
		}

		if len(entries) > 0 {
			break
		}

		if err := os.Remove(filepath.Join(out, directory)); err != nil {
			return fmt.Errorf("remove stale directory: %w", err)
		}
	}

	return nil
}
