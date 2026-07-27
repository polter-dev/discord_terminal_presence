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
		{name: "default", mask: 0o022, want: 0o644},
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

func TestInitFileForcePreservesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o644} {
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
