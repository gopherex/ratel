package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidOutput(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"catalog.sql", "main.go"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			command := exec.CommandContext(t.Context(), "go", "run", ".", "-out", filepath.Join(name, "data"))
			output, err := command.CombinedOutput()

			if err == nil || !strings.Contains(string(output), "create snapshot directory") {
				t.Fatalf("output=%s error=%v", output, err)
			}
		})
	}
}
