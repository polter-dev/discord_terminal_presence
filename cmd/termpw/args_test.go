package main

import (
	"slices"
	"testing"
)

func TestLauncherDaemonArgsOwnLogWithoutFallbackConsoleMarker(t *testing.T) {
	want := []string{"start", "--foreground", "--internal-daemon-log"}
	if !slices.Equal(launcherDaemonArgs, want) {
		t.Fatalf("launcherDaemonArgs = %q, want %q", launcherDaemonArgs, want)
	}
	if slices.Contains(launcherDaemonArgs, "--internal-autostart") {
		t.Fatal("launcher daemon args contain fallback-only console marker")
	}
}
