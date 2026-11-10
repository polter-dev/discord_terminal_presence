package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
)

// TestOneShotDetectionSkipsDisabledTools proves status and watch --once honor
// per-tool enabled = false like the daemon does, instead of featuring the
// disabled tool (issue #637).
func TestOneShotDetectionSkipsDisabledTools(t *testing.T) {
	configHome := withTermpConfigHome(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", state)
	t.Setenv("LOCALAPPDATA", state)
	t.Setenv("DISCORD_IPC_PATH", filepath.Join(t.TempDir(), "no-such-socket"))
	if err := os.MkdirAll(filepath.Join(configHome, "termp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "termp", "config.toml"), []byte("enabled = true\n[tools.nano]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute)
	restore := newOneShotLister
	newOneShotLister = func() detector.ProcessLister {
		return watchTestLister{processes: []detector.Process{
			{Pid: 101, Name: "nano", CreateTime: started, Owned: true, TTY: detector.TTYInfo{State: detector.TTYResolved, Path: "/dev/ttys001"}},
			{Pid: 102, Name: "htop", CreateTime: started, Owned: true, TTY: detector.TTYInfo{State: detector.TTYResolved, Path: "/dev/ttys002"}},
		}}
	}
	t.Cleanup(func() { newOneShotLister = restore })

	card, _, err := watchSnapshot(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(card, "idle") || !strings.Contains(card, "htop") {
		t.Errorf("watch --once card does not feature htop:\n%s", card)
	}

	out, err := captureStdout(t, func() error { return status(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Detected tool  htop") {
		t.Fatalf("status does not name htop as the detected tool:\n%s", out)
	}
}
