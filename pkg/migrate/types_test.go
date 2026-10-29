package migrate_test

import (
	"testing"

	"github.com/gopherex/ratel/pkg/migrate"
)

func TestLockMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode   migrate.LockMode
		name   string
		blocks bool
	}{
		{migrate.NoLock, "None", false},
		{migrate.AccessShare, "AccessShare", false},
		{migrate.RowShare, "RowShare", false},
		{migrate.RowExclusive, "RowExclusive", false},
		{migrate.ShareUpdateExclusive, "ShareUpdateExclusive", false},
		{migrate.Share, "Share", true},
		{migrate.ShareRowExclusive, "ShareRowExclusive", true},
		{migrate.Exclusive, "Exclusive", true},
		{migrate.AccessExclusive, "AccessExclusive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.mode.String() != tc.name || tc.mode.Blocks() != tc.blocks {
				t.Errorf("%d: %s, blocks %v", tc.mode, tc.mode, tc.mode.Blocks())
			}
		})
	}
}

// TestConflicts checks the table against PostgreSQL's documented matrix.
func TestConflicts(t *testing.T) {
	t.Parallel()

	// Each mode's row: the modes it conflicts with, weakest first.
	matrix := map[migrate.LockMode]string{
		migrate.NoLock:               "--------",
		migrate.AccessShare:          "-------X",
		migrate.RowShare:             "------XX",
		migrate.RowExclusive:         "----XXXX",
		migrate.ShareUpdateExclusive: "---XXXXX",
		migrate.Share:                "--XX-XXX",
		migrate.ShareRowExclusive:    "--XXXXXX",
		migrate.Exclusive:            "-XXXXXXX",
		migrate.AccessExclusive:      "XXXXXXXX",
	}

	for mode, row := range matrix {
		for other := migrate.AccessShare; other <= migrate.AccessExclusive; other++ {
			if want := row[other-1] == 'X'; mode.Conflicts(other) != want {
				t.Errorf("%s vs %s = %v", mode, other, !want)
			}
		}

		if mode.Conflicts(migrate.NoLock) {
			t.Errorf("%s conflicts with no lock", mode)
		}
	}
}

func BenchmarkConflicts(b *testing.B) {
	for b.Loop() {
		for mode := migrate.AccessShare; mode <= migrate.AccessExclusive; mode++ {
			migrate.ShareUpdateExclusive.Conflicts(mode)
		}
	}
}
