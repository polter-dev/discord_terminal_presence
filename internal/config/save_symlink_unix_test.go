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

func requireSymlink(t *testing.T, link string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Save replaced the symlink %s with a regular file (mode %v)", link, info.Mode())
	}
}

// #652: a dangling link whose target's directory exists (e.g. GNU Stow linked a
// dotfiles file not yet created) gets the target created and keeps the link.
func TestSaveCreatesTargetOfDanglingSymlinkedConfig(t *testing.T) {
	link, target := symlinkedConfig(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Enabled = false
	if err := Save(cfg, link); err != nil {
		t.Fatalf("Save through a dangling link: %v", err)
	}
	requireSymlink(t, link)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target was not created: %v", err)
	}
	if !strings.Contains(string(data), "enabled = false") {
		t.Fatalf("target did not receive the save: %q", data)
	}
}

// #652: a dangling chain (link -> relative link -> missing file) creates the
// final file and keeps every link in the chain.
func TestSaveFollowsDanglingSymlinkChain(t *testing.T) {
	link, target := symlinkedConfig(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(filepath.Dir(target), "real.toml")
	if err := os.Symlink("real.toml", target); err != nil {
		t.Fatal(err)
	}
	if err := Save(Default(), link); err != nil {
		t.Fatalf("Save through a dangling chain: %v", err)
	}
	requireSymlink(t, link)
	requireSymlink(t, target)
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("end of the chain was not created: %v", err)
	}
}

// #652: a dangling link whose target's directory is missing fails with an error
// naming the link and its target, and creates no directories.
func TestSaveDanglingSymlinkMissingTargetDirFailsClearly(t *testing.T) {
	link, target := symlinkedConfig(t)
	missing := filepath.Join(filepath.Dir(target), "absent", "config.toml")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	err := Save(Default(), link)
	if err == nil {
		t.Fatal("Save succeeded through a link whose target directory does not exist")
	}
	if !strings.Contains(err.Error(), link) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error should name the link %s and its target %s, got: %v", link, missing, err)
	}
	if _, statErr := os.Stat(filepath.Dir(missing)); !os.IsNotExist(statErr) {
		t.Fatalf("Save created the target directory %s", filepath.Dir(missing))
	}
	requireSymlink(t, link)
}

// #652: a read-only target (e.g. Nix Home Manager's /nix/store) fails with an
// error naming the config path, its target, and that the target is not
// writable, instead of naming a temp file.
func TestSaveReadOnlySymlinkTargetFailsClearly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	link, target := symlinkedConfig(t)
	dir := filepath.Dir(target)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	err = Save(Default(), link)
	if err == nil {
		t.Fatal("Save succeeded into a read-only target directory")
	}
	msg := err.Error()
	if !strings.Contains(msg, link) || !strings.Contains(msg, resolved) || !strings.Contains(msg, "not writable") {
		t.Fatalf("error should name %s, its target %s, and say it is not writable, got: %v", link, resolved, err)
	}
	requireSymlink(t, link)
}
