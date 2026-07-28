package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

// signallingChecker counts refreshes and announces each one, so tests can wait
// for work to happen instead of sleeping and hoping. It never installs
// anything; the runner decides that.
type signallingChecker struct {
	mu     sync.Mutex
	calls  int
	latest string
	seen   chan struct{}
}

func newSignallingChecker(latest string) *signallingChecker {
	return &signallingChecker{latest: latest, seen: make(chan struct{}, 64)}
}

func (c *signallingChecker) Refresh(_ context.Context, current string, configEnabled bool) (updatepkg.Result, bool) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	select {
	case c.seen <- struct{}{}:
	default:
	}
	if !configEnabled || c.latest == "" {
		return updatepkg.Result{}, false
	}
	return updatepkg.Result{Current: current, Latest: c.latest, Method: updatepkg.InstallGo}, true
}

func (c *signallingChecker) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// waitForRefreshes blocks until n refreshes have been observed, failing rather
// than hanging if the loop is not ticking. It asserts progress; it never
// asserts that a particular amount of wall time elapsed.
func (c *signallingChecker) waitForRefreshes(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-c.seen:
		case <-deadline:
			t.Fatalf("only %d of %d refreshes happened within the deadline", c.count(), n)
		}
	}
}

// staticConfig supplies the same config on every read.
func staticConfig(cfg config.Config) func() (config.Config, error) {
	return func() (config.Config, error) { return cfg, nil }
}

// runPeriodicInBackground starts the loop and returns a channel closed when it
// returns, so tests can prove the goroutine actually stops.
func runPeriodicInBackground(ctx context.Context, currentConfig func() (config.Config, error), checker automaticUpdateChecker, runner updatepkg.CommandRunner, interval time.Duration) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodicAutomaticUpdate(ctx, currentConfig, "1.0.0", checker, runner, interval)
	}()
	return done
}

func waitForStop(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("periodic update loop did not stop after context cancellation (goroutine leak)")
	}
}

// TestPeriodicUpdateRefreshRepeatsAndStops is the issue #460 fix: the daemon
// keeps refreshing the cache for as long as it runs, and the loop ends with the
// daemon rather than outliving it.
func TestPeriodicUpdateRefreshRepeatsAndStops(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	checker := newSignallingChecker("1.1.0")
	runner := &recordingUpdateRunner{}
	cfg := config.Default()
	cfg.UpdateCheck = true
	cfg.AutoUpdate = false

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPeriodicInBackground(ctx, staticConfig(cfg), checker, runner, time.Millisecond)

	// One startup refresh plus repeats. A single call would be the old
	// behaviour and would not get past this.
	checker.waitForRefreshes(t, 4)

	cancel()
	waitForStop(t, done)

	stopped := checker.count()
	if stopped < 4 {
		t.Fatalf("harness broken: observed %d refreshes, want at least 4", stopped)
	}
	if runner.calls != 0 {
		t.Fatalf("auto_update off still ran the update runner %d times", runner.calls)
	}
}

// TestPeriodicUpdateRefreshStopsBeforeTicking covers cancellation that arrives
// before the first tick: the startup refresh has already run, and nothing else
// may.
func TestPeriodicUpdateRefreshStopsBeforeTicking(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	checker := newSignallingChecker("1.1.0")
	cfg := config.Default()
	cfg.UpdateCheck = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := runPeriodicInBackground(ctx, staticConfig(cfg), checker, &recordingUpdateRunner{}, time.Hour)
	waitForStop(t, done)

	if got := checker.count(); got != 1 {
		t.Fatalf("cancelled loop performed %d refreshes, want exactly the startup one", got)
	}
}

// TestPeriodicUpdateRefreshHonoursOptOut is the privacy gate on the loop:
// update_check = false means no check at all, no matter how many ticks pass.
// flagIgnoringChecker reports an update regardless of the flag it is handed, so
// this measures the daemon's own gate rather than the Checker's.
func TestPeriodicUpdateRefreshHonoursOptOut(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	checker := &flagIgnoringChecker{latest: "1.1.0"}
	runner := &recordingUpdateRunner{}
	cfg := config.Default()
	cfg.UpdateCheck = false
	cfg.AutoUpdate = true

	reads := make(chan struct{}, 64)
	currentConfig := func() (config.Config, error) {
		select {
		case reads <- struct{}{}:
		default:
		}
		return cfg, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPeriodicInBackground(ctx, currentConfig, checker, runner, time.Millisecond)

	// Wait until the loop has definitely ticked several times, so "zero
	// checks" cannot be an artefact of the loop never running.
	for i := 0; i < 5; i++ {
		select {
		case <-reads:
		case <-time.After(10 * time.Second):
			t.Fatalf("harness broken: only %d config reads happened", i)
		}
	}
	cancel()
	waitForStop(t, done)

	if checker.calls != 0 {
		t.Fatalf("update_check = false still performed %d release lookups", checker.calls)
	}
	if runner.calls != 0 {
		t.Fatalf("update_check = false still ran the update runner %d times", runner.calls)
	}
}

// TestPeriodicUpdateRefreshRereadsConfig proves an opt-out made while the
// daemon runs takes effect at the next tick rather than the next restart.
func TestPeriodicUpdateRefreshRereadsConfig(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	checker := &flagIgnoringChecker{latest: "1.1.0"}
	var mu sync.Mutex
	reads := 0
	readSignal := make(chan struct{}, 64)
	currentConfig := func() (config.Config, error) {
		mu.Lock()
		reads++
		enabled := reads == 1
		mu.Unlock()
		select {
		case readSignal <- struct{}{}:
		default:
		}
		cfg := config.Default()
		cfg.UpdateCheck = enabled
		cfg.AutoUpdate = false
		return cfg, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPeriodicInBackground(ctx, currentConfig, checker, &recordingUpdateRunner{}, time.Millisecond)

	for i := 0; i < 6; i++ {
		select {
		case <-readSignal:
		case <-time.After(10 * time.Second):
			t.Fatalf("harness broken: only %d config reads happened", i)
		}
	}
	cancel()
	waitForStop(t, done)

	if checker.calls != 1 {
		t.Fatalf("checks after the config opted out = %d, want 1 (the startup refresh only)", checker.calls)
	}
}

// TestPeriodicUpdateRefreshSkipsUnreadableConfig: a config we cannot read may
// contain an opt-out, so it suppresses the check exactly like the alert paths do.
func TestPeriodicUpdateRefreshSkipsUnreadableConfig(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	checker := &flagIgnoringChecker{latest: "1.1.0"}
	reads := make(chan struct{}, 64)
	currentConfig := func() (config.Config, error) {
		select {
		case reads <- struct{}{}:
		default:
		}
		cfg := config.Default()
		cfg.UpdateCheck = true
		return cfg, os.ErrPermission
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPeriodicInBackground(ctx, currentConfig, checker, &recordingUpdateRunner{}, time.Millisecond)

	for i := 0; i < 5; i++ {
		select {
		case <-reads:
		case <-time.After(10 * time.Second):
			t.Fatalf("harness broken: only %d config reads happened", i)
		}
	}
	cancel()
	waitForStop(t, done)

	if checker.calls != 0 {
		t.Fatalf("unreadable config still performed %d release lookups", checker.calls)
	}
}

// TestPeriodicUpdateRefreshNeverInstallsWithAutoUpdateOff is #459's contract
// under repetition: a periodic refresh must not become a periodic unattended
// install for a user who turned automatic installs off.
func TestPeriodicUpdateRefreshNeverInstallsWithAutoUpdateOff(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	statePath := filepath.Join(t.TempDir(), "update-check.json")
	checker := &flagIgnoringChecker{latest: "1.1.0"}
	runner := &recordingUpdateRunner{}
	cfg := config.Default()
	cfg.UpdateCheck = true
	cfg.AutoUpdate = false

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodicAutomaticUpdate(ctx, staticConfig(cfg), "1.0.0", checker, runner, time.Millisecond)
	}()

	// Give the loop real work to repeat before judging it.
	deadline := time.After(10 * time.Second)
	for {
		if checker.callsSafe() >= 5 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("harness broken: only %d refreshes happened", checker.callsSafe())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	waitForStop(t, done)

	if runner.calls != 0 {
		t.Fatalf("auto_update off ran the update runner %d times", runner.calls)
	}
	if _, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath); ok {
		t.Fatal("cache-only refresh recorded an automatic update attempt")
	}
}

// TestDaemonUpdateRefreshIntervalIsShorterThanTheCacheLifetime states why the
// interval is what it is: at exactly the lifetime, a tick landing just before
// expiry finds the entry fresh and the cache then stays stale for nearly
// another full period.
func TestDaemonUpdateRefreshIntervalIsShorterThanTheCacheLifetime(t *testing.T) {
	if daemonUpdateRefreshInterval <= 0 || daemonUpdateRefreshInterval >= updatepkg.CacheLifetime {
		t.Fatalf("daemonUpdateRefreshInterval = %s, want a positive value below the %s cache lifetime",
			daemonUpdateRefreshInterval, updatepkg.CacheLifetime)
	}
}
