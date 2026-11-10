//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #638: saving through a symlinked config (a dotfiles setup) writes to the
// link's target and leaves the link in place.
func TestSaveWritesThroughSymlinkedConfig(t *testing.T) {
	root := t.TempDir()
	dotfiles := filepath.Join(root, "dotfiles", "termp")
	confDir := filepath.Join(root, "config", "termp")
	for _, d := range []string{dotfiles, confDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dotfiles, "config.toml")
	link := filepath.Join(confDir, "config.toml")
	if err := os.WriteFile(target, []byte("enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPath(link)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if err := Save(cfg, link); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Save replaced the symlink with a regular file (mode %v); target still reads %q", info.Mode(), targetData)
	}
	if !strings.Contains(string(targetData), "enabled = false") {
		t.Fatalf("target did not receive the save: %q", targetData)
	}
	if entries, _ := os.ReadDir(confDir); len(entries) != 1 {
		t.Fatalf("link directory should hold only the link, got %d entries", len(entries))
	}
}
