//go:build !windows

package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #638: a running daemon watching the link path must reload after Save writes
// through to the target in another directory.
func TestWatchReloadsAfterSaveThroughSymlinkedConfig(t *testing.T) {
	link, _ := symlinkedConfig(t)
	manager := NewManagerPath(link)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Watch(ctx); err != nil {
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
	timeout := time.After(3 * time.Second)
	for {
		select {
		case reload := <-manager.Reloads():
			if reload.Err == nil && !reload.Config.Enabled {
				return
			}
		case <-timeout:
			t.Fatal("no reload within 3s after Save through the symlink; daemon still has enabled=true")
		}
	}
}

func symlinkedConfig(t *testing.T) (link, target string) {
	t.Helper()
	root := t.TempDir()
	dotfiles := filepath.Join(root, "dotfiles", "termp")
	confDir := filepath.Join(root, "config", "termp")
	for _, d := range []string{dotfiles, confDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target = filepath.Join(dotfiles, "config.toml")
	link = filepath.Join(confDir, "config.toml")
	if err := os.WriteFile(target, []byte("enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

// #638: saving through a symlinked config (a dotfiles setup) writes to the
// link's target and leaves the link in place.
func TestSaveWritesThroughSymlinkedConfig(t *testing.T) {
	link, target := symlinkedConfig(t)
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
	if entries, _ := os.ReadDir(filepath.Dir(link)); len(entries) != 1 {
		t.Fatalf("link directory should hold only the link, got %d entries", len(entries))
	}
}
