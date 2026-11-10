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

// #652: a single relative dangling link (Stow style) creates its target.
func TestSaveCreatesTargetOfRelativeDanglingSymlink(t *testing.T) {
	link, target := symlinkedConfig(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../dotfiles/termp/config.toml", link); err != nil {
		t.Fatal(err)
	}
	if err := Save(Default(), link); err != nil {
		t.Fatalf("Save through a relative dangling link: %v", err)
	}
	requireSymlink(t, link)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target was not created: %v", err)
	}
}

// #652 review: a relative dangling link reached through a symlinked config
// directory resolves ".." from where the link really lives, as the kernel does,
// not textually against the unresolved path. Otherwise Save writes a stray file
// and the save is lost.
func TestSaveRelativeDanglingSymlinkThroughSymlinkedDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	realConf := filepath.Join(home, "dotfiles", "config", "termp")
	for _, d := range []string{realConf, filepath.Join(home, "dotfiles", "store"), filepath.Join(home, "store")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, "dotfiles", "config"), filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".config", "termp", "config.toml")
	if err := os.Symlink("../../store/config.toml", link); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Enabled = false
	if err := Save(cfg, link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "store", "config.toml")); err == nil {
		t.Fatal("Save wrote a stray file at home/store/config.toml instead of the link's target")
	}
	data, err := os.ReadFile(filepath.Join(home, "dotfiles", "store", "config.toml"))
	if err != nil {
		t.Fatalf("the link's real target was not created: %v", err)
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

// saveWithin runs Save with a deadline so a recursion bug fails the test
// instead of hanging the suite.
func saveWithin(t *testing.T, cfg Config, path string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- Save(cfg, path) }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Save did not return within 3s")
		return nil
	}
}

// #656 case A: a dangling link to "missing/../config.toml" must not be cleaned
// textually back into the link itself. The kernel fails on the missing
// directory, so Save fails clearly instead of recursing forever.
func TestSaveDanglingSymlinkDotDotPastMissingDirToItselfFails(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink("missing/../config.toml", link); err != nil {
		t.Fatal(err)
	}
	err := saveWithin(t, Default(), link)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want a 'does not exist' error, got: %v", err)
	}
	requireSymlink(t, link)
}

// #656 case B: a dangling link to "missing/../other.toml" never resolves, so a
// save next to the link would be lost. Save must fail, not report success.
func TestSaveDanglingSymlinkDotDotPastMissingDirFails(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink("missing/../other.toml", link); err != nil {
		t.Fatal(err)
	}
	err := saveWithin(t, Default(), link)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want a 'does not exist' error, got: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "other.toml")); statErr == nil {
		t.Fatal("Save wrote other.toml, which the link does not resolve to")
	}
	requireSymlink(t, link)
}

// #656 case C: ".." after a symlinked directory in the target resolves from the
// directory the symlink points at, as the kernel does, not textually.
func TestSaveDanglingSymlinkDotDotAfterSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{filepath.Join(root, "real", "sub"), filepath.Join(root, "real", "store"), filepath.Join(root, "store"), filepath.Join(root, "conf")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "real", "sub"), filepath.Join(root, "sublink")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "conf", "config.toml")
	if err := os.Symlink(root+"/sublink/../store/config.toml", link); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Enabled = false
	if err := saveWithin(t, cfg, link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "store", "config.toml")); err == nil {
		t.Fatal("Save wrote a stray file at root/store/config.toml instead of the link's target")
	}
	got, err := LoadPathUnsettled(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("loading through the link did not see the save")
	}
	requireSymlink(t, link)
}
