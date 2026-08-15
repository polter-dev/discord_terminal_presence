package config

import (
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
