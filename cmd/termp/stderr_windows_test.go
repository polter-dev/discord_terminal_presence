//go:build windows

package main

import (
	"errors"
	"os"
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
	if _, err := oldFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("old stderr Stat() error = %v, want os.ErrClosed", err)
	}
}
