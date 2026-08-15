package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

// TestLoadConfigWithNoticeAfterFastPathIsSilent proves the notice never
// appears when a load returns before the delay elapses — the fast path for a
// stable existing config or a missing first-run file must print nothing new
// (issue #442).
func TestLoadConfigWithNoticeAfterFastPathIsSilent(t *testing.T) {
	load := func() (config.Config, error) {
		return config.Default(), nil
	}
	var stderr bytes.Buffer
	cfg, err := loadConfigWithNoticeAfter(load, 200*time.Millisecond, &stderr)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !cfg.Enabled {
		t.Fatalf("cfg = %+v, want the loaded default config", cfg)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty on the fast path", stderr.String())
	}
}

// TestLoadConfigWithNoticeAfterSlowPathPrintsNotice proves the notice does
// appear, exactly once, when a load takes longer than the delay — this is
// the blank-config/horizon-wait/ErrConfigBeingWritten case that previously
// left the user staring at a silent prompt (issue #442).
func TestLoadConfigWithNoticeAfterSlowPathPrintsNotice(t *testing.T) {
	release := make(chan struct{})
	loadErr := errors.New("boom")
	load := func() (config.Config, error) {
		<-release
		return config.Config{}, loadErr
	}
	var stderr bytes.Buffer
	done := make(chan struct{})
	var cfg config.Config
	var err error
	go func() {
		cfg, err = loadConfigWithNoticeAfter(load, 10*time.Millisecond, &stderr)
		close(done)
	}()
	// Give the notice timer a chance to fire before the load ever returns.
	time.Sleep(80 * time.Millisecond)
	close(release)
	<-done

	if !errors.Is(err, loadErr) {
		t.Fatalf("err = %v, want %v", err, loadErr)
	}
	if cfg.Path != "" || cfg.Enabled {
		t.Fatalf("cfg = %+v, want zero value on error", cfg)
	}
	got := stderr.String()
	if strings.Count(got, "checking config") != 1 {
		t.Fatalf("stderr = %q, want exactly one checking-config notice", got)
	}
}

// TestMaybePrintCommandUpdateAlertSkipsCommandsThatLoadTheirOwnConfig proves
// main()'s pre-dispatch check does not read config a second time for setup
// and settings, which already load config for their own real work and print
// this same alert from that result. It asserts on an injected loader call
// counter, not on timing (issue #442).
func TestMaybePrintCommandUpdateAlertSkipsCommandsThatLoadTheirOwnConfig(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	restore := readOnlyConfigLoader
	readOnlyConfigLoader = func() (config.Config, bool, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return config.Default(), true, nil
	}
	t.Cleanup(func() { readOnlyConfigLoader = restore })

	var stderr bytes.Buffer
	maybePrintCommandUpdateAlert("setup", nil, true, &stderr)
	maybePrintCommandUpdateAlert("settings", nil, true, &stderr)
	if calls != 0 {
		t.Fatalf("calls = %d, want 0: setup/settings must not trigger a pre-dispatch config read", calls)
	}

	// status is never eligible for the alert (it reports update state
	// itself), so it must not trigger a pre-dispatch read either.
	maybePrintCommandUpdateAlert("status", nil, true, &stderr)
	if calls != 0 {
		t.Fatalf("calls = %d, want 0: status must not trigger a pre-dispatch config read", calls)
	}

	// Sanity check the counter actually works: an unconditionally eligible
	// command must still trigger exactly one read.
	maybePrintCommandUpdateAlert("start", nil, true, &stderr)
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 for an eligible command that does not self-load", calls)
	}
}

// TestStatusLoadsConfigExactlyOnce proves the fix for the double-settle bug
// (issue #442): a full status() invocation now reads config exactly once
// (its own read), rather than once for main()'s pre-dispatch alert check
// plus once more for status's own use. This asserts on an injected loader
// call counter, not on timing.
func TestStatusLoadsConfigExactlyOnce(t *testing.T) {
	configHome := withTermpConfigHome(t)
	// Route the Discord probe to a socket path that cannot exist so status
	// fails fast instead of waiting out the real IPC handshake timeout.
	t.Setenv("DISCORD_IPC_PATH", filepath.Join(t.TempDir(), "no-such-socket"))

	var mu sync.Mutex
	calls := 0
	restore := readOnlyConfigLoader
	readOnlyConfigLoader = func() (config.Config, bool, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return config.LoadPathReadOnly(filepath.Join(configHome, "termp", "config.toml"))
	}
	t.Cleanup(func() { readOnlyConfigLoader = restore })

	if _, err := captureStdout(t, func() error { return status(nil) }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1", calls)
	}
}
