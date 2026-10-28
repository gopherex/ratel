package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
)

func TestDecode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, text string
		fail       bool
	}{
		{"defaults", "version: 1\n", false},
		{"configured", "version: 1\npostgres: {version: 16}\nparser: {concurrency: 2}\n", false},
		{"unknown", "version: 1\nwat: 2", true},
		{"nested unknown", "version: 1\nparser: {wat: 2}", true},
		{"negative", "version: 1\nparser: {concurrency: -1}", true},
		{"postgres", "version: 1\npostgres: {version: 19}", true},
		{"missing version", "{}", true},
		{"duplicate key", "version: 1\nversion: 1", true},
		{"zero concurrency", "version: 1\nparser: {concurrency: 0}", false},
		{"migration timeout", "version: 1\nmigrate: {lock_timeout: 0s}", true},
		{"migration unknown", "version: 1\nmigrate: {backfill: {typo: 1}}", true},
		{"migration values", "version: 1\nmigrate: {lock_timeout: 200ms, backfill: {batch: 20, pause: 0s}}", false},
		{"multiple transports", "version: 1\nplugins: [{name: x, builtin: true, exec: x, out: gen}]", true},
		{"duplicate plugin", "version: 1\nplugins: [{name: x, builtin: true, out: a}, " +
			"{name: x, builtin: true, out: b}]", true},
		{"multiple documents", "version: 1\n---\nversion: 1", true},
		{"exec checksum", "version: 1\nplugins: [{name: x, exec: ./x, out: gen}]", true},
		{"duration", "version: 1\nplugins: [{name: x, exec: x, out: gen, timeout: -1s}]", true},
		{"options", "version: 1\nplugins: [{name: go, builtin: true, out: gen, options: {package: db}}]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			value, err := config.Decode(strings.NewReader(tc.text))
			if (err != nil) != tc.fail {
				t.Fatalf("%+v %v", value, err)
			}

			if err == nil && value.Parser.Concurrency < 1 {
				t.Fatal(value)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()

	value, err := config.Decode(strings.NewReader("version: 1"))
	if err != nil {
		t.Fatal(err)
	}

	if value.Postgres.Version != 18 || value.Migrate.LockTimeout != 100*time.Millisecond {
		t.Fatal(value)
	}
}

func TestDev(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, text, image, url string
		fail                   bool
	}{
		{"default", "version: 1", "postgres:18-alpine", "", false},
		{"image", "version: 1\ndev: {image: postgres:16-alpine}", "postgres:16-alpine", "", false},
		{"url", "version: 1\ndev: {url: postgres://localhost/test}", "", "postgres://localhost/test", false},
		{"both", "version: 1\ndev: {image: postgres:16-alpine, url: postgres://localhost/test}", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			value, err := config.Decode(strings.NewReader(tc.text))
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}

			if err == nil && (value.Dev.Image != tc.image || value.Dev.URL != tc.url) {
				t.Fatal(value.Dev)
			}
		})
	}
}

func BenchmarkDecode(b *testing.B) {
	for b.Loop() {
		if _, err := config.Decode(strings.NewReader("version: 1")); err != nil {
			b.Fatal(err)
		}
	}
}
