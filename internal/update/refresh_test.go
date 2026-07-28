package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// countingSource records how many release lookups were performed. It is
// safe for concurrent use so the race detector has something real to check.
type countingSource struct {
	mu     sync.Mutex
	latest string
	err    error
	calls  int
}

func (s *countingSource) Latest(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.latest, s.err
}

func (s *countingSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newTestChecker builds a checker on a private cache with a caller-controlled
// clock, so cache expiry is driven by the test rather than by wall time.
func newTestChecker(t *testing.T, source ReleaseSource, clock *time.Time) *Checker {
	t.Helper()
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	checker := NewChecker(source, filepath.Join(t.TempDir(), "update-check.json"))
	checker.Now = func() time.Time { return *clock }
	checker.DetectInstall = func() InstallMethod { return InstallGo }
	return checker
}

// TestRefreshKeepsCacheFreshOnALongLivedProcess is the issue #460 regression.
// Check performs at most one lookup per process, so a daemon that stays up past
// the cache lifetime used to leave the cache stale and the command alert silent
// forever. Refresh is what a long-lived process uses instead.
func TestRefreshKeepsCacheFreshOnALongLivedProcess(t *testing.T) {
	clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	source := &countingSource{latest: "9.9.9"}
	checker := newTestChecker(t, source, &clock)

	// Daemon start.
	if _, ok := checker.Refresh(context.Background(), "1.0.0", true); !ok {
		t.Fatal("harness broken: startup refresh found no update")
	}
	if got := source.count(); got != 1 {
		t.Fatalf("harness broken: startup lookups = %d, want 1", got)
	}
	if _, ok := checker.CachedCheck("1.0.0", true); !ok {
		t.Fatal("harness broken: cache not fresh right after the startup refresh")
	}

	// A day later the entry has expired and the alert has gone quiet. This is
	// the state the bug left the daemon in permanently.
	clock = clock.Add(25 * time.Hour)
	if _, ok := checker.CachedCheck("1.0.0", true); ok {
		t.Fatal("harness broken: cache still fresh 25h after it was written")
	}

	// The daemon refreshes again instead of sitting on a fired sync.Once.
	if _, ok := checker.Refresh(context.Background(), "1.0.0", true); !ok {
		t.Fatal("refresh after cache expiry found no update")
	}
	if got := source.count(); got != 2 {
		t.Fatalf("lookups after expiry = %d, want 2", got)
	}
	if _, ok := checker.CachedCheck("1.0.0", true); !ok {
		t.Fatal("command alert still silent after the daemon refreshed the cache")
	}
}

// TestRefreshMakesNoLookupWhileTheCacheIsFresh proves the periodic caller
// cannot raise the real lookup rate above one per cache lifetime.
func TestRefreshMakesNoLookupWhileTheCacheIsFresh(t *testing.T) {
	clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	source := &countingSource{latest: "9.9.9"}
	checker := newTestChecker(t, source, &clock)

	for i := 0; i < 6; i++ {
		checker.Refresh(context.Background(), "1.0.0", true)
		clock = clock.Add(4 * time.Hour)
	}
	if got := source.count(); got != 1 {
		t.Fatalf("lookups within one cache lifetime = %d, want 1", got)
	}
}

// TestRefreshDoesNotRetryTightlyAfterAFailure covers the offline daemon: a
// failed lookup is cached like a successful one, so ticking cannot become a
// retry storm against an unreachable source.
func TestRefreshDoesNotRetryTightlyAfterAFailure(t *testing.T) {
	clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	source := &countingSource{err: errors.New("network is unreachable")}
	checker := newTestChecker(t, source, &clock)

	for i := 0; i < 20; i++ {
		if _, ok := checker.Refresh(context.Background(), "1.0.0", true); ok {
			t.Fatal("failed lookup reported an available update")
		}
		clock = clock.Add(time.Minute)
	}
	if got := source.count(); got != 1 {
		t.Fatalf("lookups after a failure = %d, want 1 (failures are cached)", got)
	}

	// Once the recorded failure expires, one more attempt is allowed.
	clock = clock.Add(cacheLifetime)
	checker.Refresh(context.Background(), "1.0.0", true)
	if got := source.count(); got != 2 {
		t.Fatalf("lookups after the failure record expired = %d, want 2", got)
	}
}

// TestRefreshHonoursOptOuts is the privacy gate: update_check = false and
// NO_UPDATE_CHECK must both mean zero network calls, and a dev build never
// checks either. The source fails loudly so a leaked call cannot pass quietly.
func TestRefreshHonoursOptOuts(t *testing.T) {
	tests := []struct {
		name          string
		configEnabled bool
		current       string
		setEnv        bool
		env           string
	}{
		{name: "update_check_false", configEnabled: false, current: "1.0.0"},
		{name: "NO_UPDATE_CHECK_set", configEnabled: true, current: "1.0.0", setEnv: true, env: "1"},
		{name: "NO_UPDATE_CHECK_empty", configEnabled: true, current: "1.0.0", setEnv: true, env: ""},
		{name: "dev_build", configEnabled: true, current: "dev"},
		{name: "unparseable_version", configEnabled: true, current: "not-a-version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
			source := &countingSource{err: errors.New("the release source must not be called")}
			checker := newTestChecker(t, source, &clock)
			if tt.setEnv {
				t.Setenv("NO_UPDATE_CHECK", tt.env)
			}

			if _, ok := checker.Refresh(context.Background(), tt.current, tt.configEnabled); ok {
				t.Fatal("opted-out refresh reported an update")
			}
			if got := source.count(); got != 0 {
				t.Fatalf("opted-out refresh made %d release lookup(s), want 0", got)
			}
		})
	}
}

// TestRefreshDoesNotDisturbCheckMemoization protects the intra-process dedupe
// short-lived CLI runs rely on: Refresh must not republish Check's answer.
func TestRefreshDoesNotDisturbCheckMemoization(t *testing.T) {
	clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	source := &countingSource{latest: "9.9.9"}
	checker := newTestChecker(t, source, &clock)

	first, ok := checker.Check(context.Background(), "1.0.0", true)
	if !ok || first.Latest != "9.9.9" {
		t.Fatalf("harness broken: Check = %+v, %v", first, ok)
	}

	clock = clock.Add(25 * time.Hour)
	source.mu.Lock()
	source.latest = "10.0.0"
	source.mu.Unlock()
	if _, ok := checker.Refresh(context.Background(), "1.0.0", true); !ok {
		t.Fatal("harness broken: refresh after expiry found no update")
	}

	again, ok := checker.Check(context.Background(), "1.0.0", true)
	if !ok || again.Latest != first.Latest {
		t.Fatalf("Check after Refresh = %+v (%v), want the memoized %+v", again, ok, first)
	}
	if got := source.count(); got != 2 {
		t.Fatalf("total lookups = %d, want 2 (one per Check, one per Refresh)", got)
	}
}

// TestCheckStillLimitsAShortLivedRunToOneLookup states the property Refresh was
// added to avoid changing.
func TestCheckStillLimitsAShortLivedRunToOneLookup(t *testing.T) {
	clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	source := &countingSource{latest: "9.9.9"}
	checker := newTestChecker(t, source, &clock)

	for i := 0; i < 4; i++ {
		checker.Check(context.Background(), "1.0.0", true)
		clock = clock.Add(30 * time.Hour)
	}
	if got := source.count(); got != 1 {
		t.Fatalf("Check performed %d lookups in one process, want 1", got)
	}
}

// TestRefreshIsSafeUnderConcurrentCheck exercises the shared-state path the
// race detector is there to police.
func TestRefreshIsSafeUnderConcurrentCheck(t *testing.T) {
	clock := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	source := &countingSource{latest: "9.9.9"}
	checker := newTestChecker(t, source, &clock)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); checker.Check(context.Background(), "1.0.0", true) }()
		go func() { defer wg.Done(); checker.Refresh(context.Background(), "1.0.0", true) }()
	}
	wg.Wait()
	if got := source.count(); got == 0 {
		t.Fatal("harness broken: no lookups happened at all")
	}
}
