//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestInitFileCreationHonorsUmask(t *testing.T) {
	tests := []struct {
		name string
		mask int
		want os.FileMode
	}{
		{name: "restrictive", mask: 0o077, want: 0o600},
		{name: "default", mask: 0o022, want: 0o600},
		{name: "permissive", mask: 0o000, want: 0o600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMask := unix.Umask(tt.mask)
			defer unix.Umask(oldMask)

			path := filepath.Join(t.TempDir(), "config.toml")
			if err := InitFile(path, false); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tt.want {
				t.Fatalf("config mode = %04o, want %04o", got, tt.want)
			}
		})
	}
}

func TestInitFileForcePreservesExisting0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitFile(path, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %04o, want preserved 0600", got)
	}
}

func TestCopyFileBestEffortPreservesSourceMode(t *testing.T) {
	source := filepath.Join(t.TempDir(), "legacy.toml")
	destination := filepath.Join(t.TempDir(), "native", "config.toml")
	if err := os.WriteFile(source, []byte("enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFileBestEffort(source, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("migrated config mode = %04o, want source mode 0600", got)
	}
}

func TestInitFileForcePreservesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("enabled = false\n"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}

			if err := InitFile(path, true); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != mode {
				t.Fatalf("config mode = %04o, want preserved %04o", got, mode)
			}
		})
	}
}
