package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRotatingLogWriterRejectsSymlink reproduces issue #561: a preexisting
// symlink named termp.log must not silently receive daemon output. The
// symlink target must be left untouched.
func TestRotatingLogWriterRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a filesystem symlink on Windows needs elevated privileges or developer mode")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "owner-data.txt")
	if err := os.WriteFile(target, []byte("owner data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "termp.log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if _, err := newRotatingLogWriter(path, 1<<20, 3); err == nil {
		t.Fatal("newRotatingLogWriter() succeeded through a symlinked log path")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "owner data\n" {
		t.Fatalf("symlink target contents = %q, want unchanged owner data", data)
	}
}

// TestRotatingLogWriterTightensExistingMode reproduces issue #561: a
// preexisting daemon log left at a permissive mode (for example 0644 from an
// older version or a looser umask) must be tightened to 0600 on open, not
// left as-is because os.OpenFile's mode argument only applies at creation.
func TestRotatingLogWriterTightensExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits; ownership is enforced via SID instead")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "termp.log")
	if err := os.WriteFile(path, []byte("preexisting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("test setup: log mode = %v, %v; want 0644 before open", info, err)
	}

	writer, err := newRotatingLogWriter(path, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("existing daemon log mode = %o, want 0600", got)
	}
}

// TestRotatingLogWriterTightensExistingDirectoryMode covers the related gap
// the issue calls out: os.MkdirAll is a no-op on an already-existing
// directory, so a log directory created earlier under a looser umask stayed
// loose across restarts before this fix.
func TestRotatingLogWriterTightensExistingDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits; directory ACLs are left at the platform default")
	}
	dir := filepath.Join(t.TempDir(), "termp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "termp.log")

	writer, err := newRotatingLogWriter(path, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("existing daemon log directory mode = %o, want 0700", got)
	}
}
