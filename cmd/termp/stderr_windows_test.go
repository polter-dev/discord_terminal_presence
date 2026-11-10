//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRedirectStderrClosesPreviousFile(t *testing.T) {
	previousStderr := os.Stderr
	previousHandle, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		t.Fatal(err)
	}
	oldFile, err := os.CreateTemp(t.TempDir(), "old-stderr")
	if err != nil {
		t.Fatal(err)
	}
	newFile, err := os.CreateTemp(t.TempDir(), "new-stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, previousHandle)
		_ = oldFile.Close()
		_ = newFile.Close()
	})

	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(oldFile.Fd())); err != nil {
		t.Fatal(err)
	}
	os.Stderr = oldFile

	if err := redirectStderr(newFile); err != nil {
		t.Fatal(err)
	}
	if os.Stderr != newFile {
		t.Fatalf("os.Stderr = %p, want %p", os.Stderr, newFile)
	}
	if err := oldFile.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("second old stderr Close() error = %v, want os.ErrClosed", err)
	}
}

// TestAutostartConsoleReleaseKeepsLogLockHandle reproduces #663. FreeConsole
// frees the std handle numbers while os.Stderr still holds one; the log lock
// file then reopens that number, and redirectStderr used to close it, so every
// managed log write failed with "The handle is invalid."
func TestAutostartConsoleReleaseKeepsLogLockHandle(t *testing.T) {
	restoreStdFilesAfter(t)
	previousHandle, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, previousHandle) })
	dir := t.TempDir()
	// Handle numbers are reused most-recently-freed first, but another thread
	// can take the number in between, so repeat to make the reuse near certain.
	for attempt := 0; attempt < 10; attempt++ {
		console, err := os.CreateTemp(dir, "console")
		if err != nil {
			t.Fatal(err)
		}
		stale := windows.Handle(console.Fd())
		if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, stale); err != nil {
			t.Fatal(err)
		}
		os.Stderr = console
		// Stands in for FreeConsole: the handle closes under a still-open *os.File.
		freeConsole := func() error { return windows.CloseHandle(stale) }
		if err := releaseAutostartConsoleWith(io.Discard, func(string) error { return nil }, freeConsole); err != nil {
			t.Fatal(err)
		}
		writer, err := newRotatingLogWriter(filepath.Join(dir, fmt.Sprintf("termp-%d.log", attempt)), detachedLogMaxBytes, detachedLogRetained)
		if err != nil {
			t.Fatal(err)
		}
		lockReusedStderr := windows.Handle(writer.lockFile.Fd()) == stale
		if err := writer.RedirectStderr(); err != nil {
			t.Fatal(err)
		}
		_, writeErr := writer.Write([]byte("daemon started\n"))
		_ = writer.Close()
		if writeErr != nil {
			t.Fatalf("attempt %d: log write after console release: %v (lock file reused the released stderr handle: %v)", attempt, writeErr, lockReusedStderr)
		}
	}
}
