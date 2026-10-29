package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/authoring"
)

// handWritten is the body of a file ratel migrate new creates.
const handWritten = `-- Hand-written migration. Each statement below that no step header covers
-- is a step of its own in the expand phase. A header starts a step holding
-- the statements up to the next header:
--   -- ratel:step phase=backfill|contract
-- -- @ratel.allow <RTL-code> reason=<text> before a statement accepts a lint
-- finding on it. Run ratel migrate hash after editing: it seals each step
-- with its transaction mode, class and locks and updates ratel.sum.
`

// create writes an empty hand-written migration and records it in
// ratel.sum.
func (w *workspace) create(name string) error {
	file := migrate.FileName(time.Now(), name)
	w.sources = append(w.sources, migrate.Source{Name: file, SQL: handWritten})

	if err := atomicWrite(filepath.Join(w.directory, file), []byte(handWritten), fileMode); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}

	if err := w.writeSum(); err != nil {
		return err
	}

	fmt.Fprintf(w.stdout, "wrote %s\n", file)

	return nil
}

// hash seals every hand-written step with what its statements do and
// records the files in ratel.sum.
func (w *workspace) hash() error {
	files, err := w.files()
	if err != nil {
		return err
	}

	for index := range files {
		file := &files[index]

		sealed := authoring.Seal(file, w.settings.Postgres.Version)
		if sealed == file.SQL {
			continue
		}

		if err := atomicWrite(filepath.Join(w.directory, file.Name), []byte(sealed), fileMode); err != nil {
			return fmt.Errorf("write %s: %w", file.Name, err)
		}

		w.sources[index].SQL = sealed

		fmt.Fprintf(w.stdout, "sealed %s\n", file.Name)
	}

	return w.writeSum()
}

// writeSum records the migration files in ratel.sum.
func (w *workspace) writeSum() error {
	if err := atomicWrite(filepath.Join(w.directory, migrate.SumFile), migrate.Sum(w.sources), fileMode); err != nil {
		return fmt.Errorf("write %s: %w", migrate.SumFile, err)
	}

	return nil
}
