package migrate

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// SumFile is the integrity file of a migrations directory.
const SumFile = "ratel.sum"

// directoryLine names the hash of all file lines.
const directoryLine = "directory"

// ErrSum is a migrations directory that does not match its ratel.sum.
var ErrSum = errors.New("migrations do not match " + SumFile)

// Source is the contents of one migration file.
type Source struct{ Name, SQL string }

// Read lists the migration files of a directory in name order.
func Read(directory fs.FS) ([]Source, error) {
	names, err := fs.Glob(directory, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}

	slices.Sort(names)

	sources := make([]Source, 0, len(names))

	for _, name := range names {
		data, err := fs.ReadFile(directory, name)
		if err != nil {
			return nil, fmt.Errorf("migrations: %w", err)
		}

		sources = append(sources, Source{Name: path.Base(name), SQL: string(data)})
	}

	return sources, nil
}

// Sum is the ratel.sum of the files: a hash per file, then the hash of
// those lines, which changes with any edit, rename or removal.
func Sum(sources []Source) []byte {
	var lines strings.Builder

	for _, source := range sources {
		lines.WriteString(source.Name + " " + hash(source.SQL) + "\n")
	}

	return []byte(lines.String() + directoryLine + " " + hash(lines.String()) + "\n")
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(text))

	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

// Verify compares the files with the recorded ratel.sum and names every
// file edited, added or missing.
func Verify(sources []Source, recorded []byte) error {
	want := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	got := strings.Split(strings.TrimSpace(string(Sum(sources))), "\n")

	if slices.Equal(want, got) {
		return nil
	}

	expected := sums(want)
	actual := sums(got)

	var problems []string

	for _, source := range sources {
		switch recordedHash, ok := expected[source.Name]; {
		case !ok:
			problems = append(problems, source.Name+": not in "+SumFile)
		case recordedHash != actual[source.Name]:
			problems = append(problems, source.Name+": edited")
		default:
		}
	}

	for name := range expected {
		if _, ok := actual[name]; !ok && name != directoryLine {
			problems = append(problems, name+": missing")
		}
	}

	slices.Sort(problems)

	if len(problems) == 0 {
		problems = append(problems, SumFile+": edited")
	}

	return fmt.Errorf("%w: %s", ErrSum, strings.Join(problems, "; "))
}

func sums(lines []string) map[string]string {
	result := make(map[string]string, len(lines))

	for _, line := range lines {
		name, value, _ := strings.Cut(line, " ")
		result[name] = value
	}

	return result
}
