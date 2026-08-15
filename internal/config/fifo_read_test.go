//go:build !windows

// FIFO and symlink behavior is exercised on Unix only: syscall.Mkfifo does
// not exist on Windows, and creating a symlink there needs elevation the CI
// runner does not have.

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestHuntFifoConfigPathDoesNotHang is the #576 reproduction: a named pipe
// with no writer at the config path used to block os.Open inside
// snapshotConfigFile forever, since it opened with no O_NONBLOCK and no mode
// check. Every settled-read path funnels through snapshotConfigFile
// (LoadPath, LoadPathReadOnly, LoadPathUnsettled, manager construction, and
// Manager.Reload), so this hung the whole read path. Manager.Reload also
// holds reloadMu on the fsnotify goroutine, so the watcher stopped too.
// Unix only: Windows named pipes are a different object entirely and are
// not reachable through a filesystem path the same way.
func TestHuntFifoConfigPathDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs are a Unix filesystem concept; not reproducible on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo(%s) = %v", path, err)
	}

	type result struct {
		cfg     Config
		settled bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		cfg, settled, err := LoadPathReadOnly(path)
		done <- result{cfg: cfg, settled: settled, err: err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatalf("LoadPathReadOnly() on a FIFO config path = nil error, want a refusal error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UNBOUNDED: LoadPathReadOnly on a FIFO config path did not return within 5s")
	}
}

// TestHuntFifoConfigPathDoesNotHangManagerConstruction is the manager
// construction counterpart: NewManagerPath must not hang either, since a
// stuck construction call also means a stuck daemon startup.
func TestHuntFifoConfigPathDoesNotHangManagerConstruction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs are a Unix filesystem concept; not reproducible on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo(%s) = %v", path, err)
	}

	done := make(chan *Manager, 1)
	go func() {
		done <- NewManagerPath(path)
	}()

	select {
	case <-done:
		// Construction must complete (fail-closed, presence off), not hang.
	case <-time.After(5 * time.Second):
		t.Fatal("UNBOUNDED: NewManagerPath on a FIFO config path did not return within 5s")
	}
}

// TestSymlinkedConfigPathStillReadable is the lead-review regression for the
// #576 fix: pointing the config path at a symlink (a common dotfiles-repo
// setup, where ~/.config/termp/config.toml is a symlink into a separately
// version-controlled directory) must keep working. The first cut of the fix
// used os.Lstat, which does not follow symlinks and refused every symlinked
// config outright. Reading through a symlink is equivalent to reading
// whatever the user could already open by that path directly, so it is not
// a new capability the way InitFile's write-side symlink refusal defends
// against, and snapshotConfigFile now uses os.Stat (which follows the link)
// instead.
func TestSymlinkedConfigPathStillReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated privileges on Windows by default")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real-config.toml")
	writeConfig(t, real, "enabled = true\n")

	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink(%s, %s) = %v", real, link, err)
	}

	cfg, settled, err := LoadPathReadOnly(link)
	if err != nil {
		t.Fatalf("LoadPathReadOnly() on a symlinked config path = %v, want a successful load", err)
	}
	if !settled {
		t.Fatal("LoadPathReadOnly() on a symlinked config path = unsettled, want settled")
	}
	if !cfg.Enabled {
		t.Fatal("LoadPathReadOnly() on a symlinked config path = enabled false, want true")
	}
}

// TestSymlinkToFifoConfigPathDoesNotHang confirms the symlink fix does not
// reopen the #576 hang: a symlink whose target is a FIFO, not a regular
// file, must still be refused. os.Stat follows the link and reports the
// FIFO's mode, so this is caught the same way a direct FIFO is.
func TestSymlinkToFifoConfigPathDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs are a Unix filesystem concept; not reproducible on Windows")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "real-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo(%s) = %v", fifo, err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "config.toml")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatalf("Symlink(%s, %s) = %v", fifo, link, err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := LoadPathReadOnly(link)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("LoadPathReadOnly() on a symlink-to-FIFO config path = nil error, want a refusal error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UNBOUNDED: LoadPathReadOnly on a symlink-to-FIFO config path did not return within 5s")
	}
}
