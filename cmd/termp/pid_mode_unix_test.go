//go:build !windows

package main

import (
	"os"
	"testing"
)

func assertPIDDirectoryMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("PID directory mode = %o, want 700", got)
	}
}

func assertPIDFileMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("PID file mode = %o, want 600", got)
	}
}
