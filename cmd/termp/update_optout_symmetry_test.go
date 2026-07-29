package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

// loudReleaseSource fails the test the moment the release source is consulted.
// A counter that is merely asserted to be zero at the end can be silently
// defeated by a test that returns early; this cannot.
type loudReleaseSource struct {
	t *testing.T
}

func (s *loudReleaseSource) Latest(context.Context, string) (string, error) {
	s.t.Helper()
	s.t.Error("release source consulted while an update-check opt-out was in force")
	return "0.1.3", nil
}

// seedRetirableState plants both halves of the state the shipped opt-out tests
// were missing: a failed attempt for a target that is no longer on offer AND a
// cache that already records what the source last offered.
//
// The second half is the whole point. retireStaleAutomaticUpdateAttempt draws
// no conclusion from an empty cache, so an opt-out test seeded with only an
// attempt record cannot observe whether retirement ran at all — it reports "not
// cleared" either way. Every assertion below therefore depends on this state
// being genuinely retirable, which the control case proves.
//
// The bytes are written directly rather than through the writer API because
// there is no exported way to plant a cached latest, and because the on-disk
// format is what the code under test actually reads.
func seedRetirableState(t *testing.T, target, cachedLatest string) string {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "update-check.json")
	seedRetirableStateAt(t, statePath, target, cachedLatest)
	return statePath
}

func seedRetirableStateAt(t *testing.T, statePath, target, cachedLatest string) {
	t.Helper()
	entry := fmt.Sprintf(
		`{"checked_at":%q,"latest_version":%q,"automatic_update":{"attempted_at":"2026-07-28T17:46:02Z","target_version":%q,"error":"download installer: exec failed"}}`,
		time.Now().UTC().Format(time.RFC3339), cachedLatest, target)
	if err := os.WriteFile(statePath, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath); !ok {
		t.Fatalf("harness broken: no attempt recorded at %s", statePath)
	}
	if got := updatepkg.LastKnownLatest(statePath); got != cachedLatest {
		t.Fatalf("harness broken: LastKnownLatest = %q, want %q", got, cachedLatest)
	}
}

func attemptSurvives(t *testing.T, statePath string) bool {
	t.Helper()
	_, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
	return ok
}

// The two tests below pin issue #463: NO_UPDATE_CHECK and update_check = false
// must both make the automatic-update path completely inert — no network call
// and no state-file mutation.
//
// Before the fix the env opt-out did not return early, so retirement still ran
// and cleared the record; only the config opt-out was inert. The shipped
// env-opt-out test used an empty cache, where retirement declines to act, so it
// passed under both behaviours and pinned neither.
const (
	symmetryCurrent      = "0.1.2" // explicit, so isDevVersion never applies
	symmetryTarget       = "1.1.0" // recorded, never published: retirable
	symmetryCachedLatest = "0.1.3" // what the source last offered
)

// TestAutomaticUpdateRetiresSeededStateWithoutAnOptOut is the control. It
// proves the seeded state is one that retirement really does clear, which is
// what makes the two opt-out cases below meaningful rather than vacuous.
func TestAutomaticUpdateRetiresSeededStateWithoutAnOptOut(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	statePath := seedRetirableState(t, symmetryTarget, symmetryCachedLatest)
	source := &staticReleaseSource{latest: symmetryCachedLatest}
	checker := updatepkg.NewChecker(source, statePath)
	checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGo }
	cfg := config.Default()
	cfg.AutoUpdate = false

	runAutomaticUpdateWithStatePathForPlatform(context.Background(), cfg, symmetryCurrent, checker, &recordingUpdateRunner{}, statePath, "linux")

	if attemptSurvives(t, statePath) {
		t.Fatal("harness broken: the seeded record is not retirable, so the opt-out tests would pass vacuously")
	}
}

// TestAutomaticUpdateOptOutsLeaveStateAlone runs the identical retirable state
// through both opt-outs. Neither may call out and neither may write.
func TestAutomaticUpdateOptOutsLeaveStateAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    bool
		envVal string
	}{
		{name: "config_update_check_false"},
		{name: "env_NO_UPDATE_CHECK_set", env: true, envVal: "1"},
		// Any value opts out, including the empty string, and that rule has
		// to hold at this level too.
		{name: "env_NO_UPDATE_CHECK_empty", env: true, envVal: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })
			cfg := config.Default()
			cfg.AutoUpdate = false
			if tc.env {
				t.Setenv("NO_UPDATE_CHECK", tc.envVal)
			} else {
				cfg.UpdateCheck = false
			}

			// Real Checker: proves no network call on the real path.
			statePath := seedRetirableState(t, symmetryTarget, symmetryCachedLatest)
			checker := updatepkg.NewChecker(&loudReleaseSource{t: t}, statePath)
			checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGo }
			runAutomaticUpdateWithStatePathForPlatform(context.Background(), cfg, symmetryCurrent, checker, &recordingUpdateRunner{}, statePath, "linux")
			if !attemptSurvives(t, statePath) {
				t.Error("attempt cleared under an update-check opt-out: the subsystem must not touch state")
			}

			// Checker stub that honours no opt-out at all: proves the gate is
			// enforced by runAutomaticUpdate itself, not borrowed from Checker.
			stubState := seedRetirableState(t, symmetryTarget, symmetryCachedLatest)
			stub := &flagIgnoringChecker{latest: symmetryCachedLatest}
			runAutomaticUpdateWithStatePathForPlatform(context.Background(), cfg, symmetryCurrent, stub, &recordingUpdateRunner{}, stubState, "linux")
			if stub.callsSafe() != 0 {
				t.Errorf("checker called %d times under an update-check opt-out, want 0", stub.callsSafe())
			}
			if !attemptSurvives(t, stubState) {
				t.Error("attempt cleared under an update-check opt-out via a checker that ignores the flag")
			}

			// #465's periodic loop shares the same gate; a ticking daemon must
			// not reopen either hole. The loop reaches the state file through
			// DefaultCachePath rather than an injected path, so the cache home
			// is redirected here and the seeded state planted where the loop
			// will actually look.
			cacheHome := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cacheHome)
			loopState := updatepkg.DefaultCachePath()
			if err := os.MkdirAll(filepath.Dir(loopState), 0o700); err != nil {
				t.Fatal(err)
			}
			seedRetirableStateAt(t, loopState, symmetryTarget, symmetryCachedLatest)
			loopStub := &flagIgnoringChecker{latest: symmetryCachedLatest}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				runPeriodicAutomaticUpdate(ctx, staticConfig(cfg), symmetryCurrent, loopStub, &recordingUpdateRunner{}, time.Millisecond)
			}()
			time.Sleep(50 * time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("periodic update loop did not stop after cancellation")
			}
			if loopStub.callsSafe() != 0 {
				t.Errorf("periodic loop called the checker %d times under an update-check opt-out, want 0", loopStub.callsSafe())
			}
			if !attemptSurvives(t, loopState) {
				t.Error("periodic loop cleared the attempt under an update-check opt-out")
			}
		})
	}
}
