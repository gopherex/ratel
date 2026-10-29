package migrate_test

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gopherex/ratel/pkg/migrate"
)

func TestLockKey(t *testing.T) {
	t.Parallel()

	key := migrate.LockKey("app")
	if len(key) != 64 || strings.Trim(key, "01") != "" || key == migrate.LockKey("other") {
		t.Errorf("lockKey() = %q", key)
	}
}

func TestSpread(t *testing.T) {
	t.Parallel()

	for range 100 {
		if got := migrate.Spread(time.Second); got < 750*time.Millisecond || got >= 1250*time.Millisecond {
			t.Fatalf("spread() = %v", got)
		}
	}

	if migrate.Spread(0) != 0 {
		t.Error("spread(0) is not 0")
	}
}

func TestChecksum(t *testing.T) {
	t.Parallel()

	if migrate.Checksum([]string{"a", "b"}) == migrate.Checksum([]string{"a;\nb"}) &&
		migrate.Checksum([]string{"a"}) == migrate.Checksum([]string{"b"}) {
		t.Error("checksum() collides")
	}
}

// sealedFile is a sealed one-step file.
const sealedFile = "-- ratel:step id=a1 phase=expand tx=in class=short pg=16\nSELECT 1;\n"

func TestCheck(t *testing.T) {
	t.Parallel()

	withSum := func(files fstest.MapFS) fstest.MapFS {
		sources := make([]migrate.Source, 0, len(files))
		for name, file := range files {
			sources = append(sources, migrate.Source{Name: name, SQL: string(file.Data)})
		}

		slices.SortFunc(sources, func(a, b migrate.Source) int { return strings.Compare(a.Name, b.Name) })
		files[migrate.SumFile] = &fstest.MapFile{Data: migrate.Sum(sources)}

		return files
	}

	tampered := withSum(fstest.MapFS{"1_a.sql": {Data: []byte(sealedFile)}})
	tampered["1_a.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE app.t;")}

	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		want  error
	}{
		{"sealed", withSum(fstest.MapFS{"1_a.sql": {Data: []byte(sealedFile)}}), nil},
		{"empty", withSum(fstest.MapFS{}), nil},
		{"unsealed", withSum(fstest.MapFS{"1_a.sql": {Data: []byte("SELECT 1;")}}), migrate.ErrUnsealed},
		{"tampered", tampered, migrate.ErrSum},
		{"bad header", withSum(fstest.MapFS{"1_a.sql": {Data: []byte("-- ratel:step tx=x\nSELECT 1;")}}), migrate.ErrHeader},
		{"no sum", fstest.MapFS{"1_a.sql": {Data: []byte(sealedFile)}}, fs.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := migrate.Check(tc.files); !errors.Is(err, tc.want) {
				t.Errorf("Check() = %v, want %v", err, tc.want)
			}
		})
	}
}

func BenchmarkCheck(b *testing.B) {
	files := fstest.MapFS{"1_a.sql": {Data: []byte(sealedFile)}}
	files[migrate.SumFile] = &fstest.MapFile{Data: migrate.Sum([]migrate.Source{{Name: "1_a.sql", SQL: sealedFile}})}

	for b.Loop() {
		if err := migrate.Check(files); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLockKey(b *testing.B) {
	for b.Loop() {
		migrate.LockKey("app")
	}
}
