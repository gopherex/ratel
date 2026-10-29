// Package config loads strict versioned ratel.yaml configuration.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// Error is invalid user configuration.
type Error struct{ Message string }

func (e *Error) Error() string { return "ratel.yaml: " + e.Message }

// Config is the version-one configuration contract.
type Config struct {
	Version    int          `json:"version"`
	Postgres   Postgres     `json:"postgres"`
	Parser     parse.Config `json:"parser"`
	Schema     []string     `json:"schema"`
	Queries    []Query      `json:"queries"`
	Migrations string       `json:"migrations"`
	Dev        Dev          `json:"dev"`
	Plugins    []Plugin     `json:"plugins"`
	Migrate    Migrate      `json:"migrate"`
}

type Postgres struct {
	Version int `json:"version"`
}
type Query struct {
	Files   []string `json:"files"`
	Package string   `json:"package"`
}
type Dev struct {
	Image string `json:"image"`
	URL   string `json:"url"`
}

// Plugin selects exactly one transport. Options are encoded as JSON for replay.
type Plugin struct {
	Name    string         `json:"name"`
	Builtin bool           `json:"builtin"`
	Exec    string         `json:"exec"`
	Wasm    string         `json:"wasm"`
	SHA256  string         `json:"sha256"`
	Out     string         `json:"out"`
	Timeout time.Duration  `json:"timeout"`
	Options map[string]any `json:"options"`
}

type Migrate struct {
	LockTimeout      time.Duration `json:"lock_timeout"`
	RetrySchedule    string        `json:"retry_schedule"`
	MaxTotalWait     time.Duration `json:"max_total_wait"`
	StatementTimeout time.Duration `json:"statement_timeout"`
	LongTxThreshold  time.Duration `json:"long_tx_threshold"`
	Backfill         Backfill      `json:"backfill"`
}
type Backfill struct {
	Batch         int           `json:"batch"`
	Pause         time.Duration `json:"pause"`
	MaxReplicaLag time.Duration `json:"max_replica_lag"`
}

// Load reads one configuration file without changing the working directory.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}

	defer file.Close()

	return Decode(file)
}

// Decode rejects unknown keys, duplicate keys and additional YAML documents.
func Decode(reader io.Reader) (Config, error) {
	value := defaults()
	decoder := yaml.NewDecoder(reader, yaml.Strict())

	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, &Error{Message: "expected exactly one YAML document"}
	}

	if value.Dev.Image == "" && value.Dev.URL == "" {
		value.Dev.Image = "postgres:18-alpine"
	}

	if value.Parser.Concurrency == 0 {
		value.Parser.Concurrency = 1
	}

	for index := range value.Plugins {
		if value.Plugins[index].Timeout == 0 {
			value.Plugins[index].Timeout = time.Minute
		}
	}

	if err := validate(value); err != nil {
		return Config{}, err
	}

	return value, nil
}

func defaults() Config {
	run := migrate.DefaultSettings()

	return Config{
		Postgres: Postgres{Version: migrate.LatestMajor}, Parser: parse.Config{Concurrency: parse.DefaultConcurrency},
		Schema: []string{"schema/**/*.sql"}, Migrations: "migrations",
		Migrate: Migrate{
			LockTimeout: run.LockTimeout, RetrySchedule: "gitlab", MaxTotalWait: run.MaxTotalWait,
			StatementTimeout: run.StatementTimeout, LongTxThreshold: run.LongTxThreshold,
			Backfill: Backfill{
				Batch: run.Backfill.Batch, Pause: run.Backfill.Pause, MaxReplicaLag: run.Backfill.MaxReplicaLag,
			},
		},
	}
}

func validate(value Config) error {
	if value.Version != 1 {
		return &Error{Message: "version must be 1"}
	}

	if value.Postgres.Version < 16 || value.Postgres.Version > 18 {
		return &Error{Message: "postgres.version must be 16, 17 or 18"}
	}

	if value.Parser.Concurrency < 0 {
		return &Error{Message: "parser.concurrency must not be negative"}
	}

	if value.Dev.Image != "" && value.Dev.URL != "" {
		return &Error{Message: "dev.image and dev.url are mutually exclusive"}
	}

	if err := validateMigrate(value.Migrate); err != nil {
		return err
	}

	names := make(map[string]bool, len(value.Plugins))
	for _, entry := range value.Plugins {
		if names[entry.Name] {
			return &Error{Message: "duplicate plugin " + entry.Name}
		}

		names[entry.Name] = true

		if err := ValidatePlugin(entry); err != nil {
			return err
		}
	}

	return nil
}

func validateMigrate(value Migrate) error {
	if value.LockTimeout <= 0 || value.MaxTotalWait <= 0 || value.StatementTimeout <= 0 ||
		value.LongTxThreshold <= 0 || value.Backfill.Batch <= 0 || value.Backfill.Pause < 0 ||
		value.Backfill.MaxReplicaLag <= 0 || value.RetrySchedule != "gitlab" {
		return &Error{Message: "invalid migrate timeout, retry schedule or backfill configuration"}
	}

	return nil
}

// ValidatePlugin validates transports even when constructed outside YAML loading.
func ValidatePlugin(entry Plugin) error {
	count := 0
	if entry.Builtin {
		count++
	}

	if entry.Exec != "" {
		count++
	}

	if entry.Wasm != "" {
		count++
	}

	if count != 1 || entry.Name == "" || entry.Out == "" || entry.Timeout <= 0 {
		return &Error{Message: "plugin requires name, out, positive timeout and exactly one of builtin, exec, wasm"}
	}

	required := entry.Wasm != "" || IsPath(entry.Exec)
	digest, err := hex.DecodeString(entry.SHA256)

	if (required || entry.SHA256 != "") && (err != nil || len(digest) != 32) {
		return &Error{Message: "plugin " + entry.Name + " requires a 64-digit sha256"}
	}

	return nil
}

// IsPath distinguishes an executable path from a PATH lookup.
func IsPath(value string) bool { return filepath.IsAbs(value) || strings.ContainsAny(value, "/\\") }
