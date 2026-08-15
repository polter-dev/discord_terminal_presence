//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/service"
)

func TestStopBoundsHungLaunchctlStatusProbe(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	runtimeDir := filepath.Join(root, "run")
	stateDir := filepath.Join(root, "state")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{home, runtimeDir, stateDir, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	markerPath := filepath.Join(root, "launchctl-called")
	t.Setenv("TERMP_FAKE_LAUNCHCTL_CALLED", markerPath)
	launchctlPath := filepath.Join(binDir, "launchctl")
	launchctl := "#!/bin/sh\n: > \"$TERMP_FAKE_LAUNCHCTL_CALLED\"\nexec sleep 6\n"
	if err := os.WriteFile(launchctlPath, []byte(launchctl), 0o755); err != nil {
		t.Fatal(err)
	}

	exe, err := service.ResolveExecutable()
	if err != nil {
		t.Fatal(err)
	}
	plist, err := service.BuildLaunchAgentPlist(exe)
	if err != nil {
		t.Fatal(err)
	}
	launchAgents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchAgents, service.Label+".plist"), plist, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFilePath(), []byte("unreadable"), 0o600); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	err = stop(nil)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed >= stopTimeout {
		t.Fatalf("stop() elapsed = %v, want less than %v", elapsed, stopTimeout)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("fake launchctl was not called: %v", err)
	}
}
