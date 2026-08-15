//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsTermpSiblingIsAbsoluteAndBypassesInvokedName(t *testing.T) {
	image := filepath.Join(t.TempDir(), "scoop-shim-name.exe")
	got, err := windowsTermpSibling(image)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(image), "termp.exe")
	if got != want || !filepath.IsAbs(got) {
		t.Fatalf("windowsTermpSibling(%q) = %q, want absolute %q", image, got, want)
	}
}

func TestConfigureDetachedProcessSuppressesConsoleWindow(t *testing.T) {
	command := exec.Command("termp.exe")
	configureDetachedProcess(command)
	want := uint32(syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess | createNoWindow)
	if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags != want {
		t.Fatalf("CreationFlags = %#x, want %#x", command.SysProcAttr.CreationFlags, want)
	}
}
