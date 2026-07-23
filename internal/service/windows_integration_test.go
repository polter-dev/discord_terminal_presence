//go:build windows && integration

package service

import (
	"os"
	"testing"
)

func TestWindowsSchtasksIntegration(t *testing.T) {
	manager := NewManager()
	if manager.GOOS != "windows" {
		t.Fatalf("NewManager().GOOS = %q, want windows", manager.GOOS)
	}

	t.Cleanup(func() {
		if _, err := manager.Uninstall(); err != nil {
			t.Errorf("cleanup Uninstall() error = %v", err)
		}
	})

	// Remove a task left behind by an interrupted earlier run before asserting
	// the complete lifecycle from a known state.
	state, err := manager.Uninstall()
	if err != nil {
		t.Fatalf("initial Uninstall() error = %v", err)
	}
	assertWindowsIntegrationState(t, "initial uninstall", state, false, "false", "false")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	state, err = manager.Install(exe)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	assertWindowsIntegrationState(t, "install", state, true, "true", "unknown", "true")

	// Query separately so the test directly covers the /XML status path. The
	// task may not launch a process on a headless CI runner, so Loaded may stay
	// unknown and is not used as proof that installation succeeded.
	state = manager.Status()
	assertWindowsIntegrationState(t, "status", state, true, "true", "unknown", "true")

	state, err = manager.Disable()
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	assertWindowsIntegrationState(t, "disable", state, true, "false", "unknown", "false")

	state, err = manager.Enable()
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	assertWindowsIntegrationState(t, "enable", state, true, "true", "unknown", "true")

	state, err = manager.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	assertWindowsIntegrationState(t, "uninstall", state, false, "false", "false")
}

func assertWindowsIntegrationState(
	t *testing.T,
	step string,
	state State,
	installed bool,
	enabled string,
	allowedLoaded ...string,
) {
	t.Helper()

	if !state.Supported {
		t.Fatalf("%s: Supported = false", step)
	}
	if state.Path != TaskName {
		t.Fatalf("%s: Path = %q, want %q", step, state.Path, TaskName)
	}
	if state.Message != "" {
		t.Fatalf("%s: Message = %q, want empty", step, state.Message)
	}
	if state.Installed != installed {
		t.Fatalf("%s: Installed = %t, want %t", step, state.Installed, installed)
	}
	if state.Enabled != enabled {
		t.Fatalf("%s: Enabled = %q, want %q", step, state.Enabled, enabled)
	}
	for _, allowed := range allowedLoaded {
		if state.Loaded == allowed {
			return
		}
	}
	t.Fatalf("%s: Loaded = %q, want one of %v", step, state.Loaded, allowedLoaded)
}
