package config

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Lifecycle tests drive the real loosening retry, so they need a horizon they
// can wait out. Milliseconds rather than the production three seconds, wide
// enough that a loaded CI machine still schedules the timer comfortably inside
// the wait budget below.
const (
	closeTestHorizon = 120 * time.Millisecond
	closeTestWait    = 20 * closeTestHorizon
)

// newFirstRunManager builds a manager on a path with no config file. That is
// the ordinary first-run construction path since #551: presence is seeded off
// and the loosening retry is armed to reach enabled defaults one horizon
// later, without any filesystem event. It is therefore the cheapest way to get
// a manager whose armed retry has a visible effect a test can watch for.
func newFirstRunManager(t *testing.T, path string) *Manager {
	t.Helper()
	manager := newManagerPathWithHorizon(
		path,
		snapshotConfigFile,
		time.Now,
		time.Sleep,
		closeTestHorizon,
	)
	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("precondition not established: Current() at construction = %v, want no error", err)
	}
	if cfg.Enabled {
		t.Fatal("precondition not established: first-run construction seeded presence on, so the retry has no observable effect")
	}
	assertManagerLooseningGuardArmed(t, manager, "at first-run construction")
	return manager
}

// waitForEnabled polls until the manager's current config turns presence on,
// which only the armed loosening retry can do here: nothing writes the file
// and no watcher is installed.
func waitForEnabled(t *testing.T, manager *Manager, budget time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		cfg, err := manager.Current()
		if err != nil {
			t.Fatalf("Current() while waiting for the retry = %v", err)
		}
		if cfg.Enabled {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestManagerCloseStopsPendingLooseningRetry is the #553 subject, paired with
// its own positive control. The control proves the retry really does fire on
// its own and flip presence on, so the closed case failing to flip is evidence
// the timer was stopped rather than evidence the test cannot fail.
func TestManagerCloseStopsPendingLooseningRetry(t *testing.T) {
	t.Run("control: an open manager lets the retry fire", func(t *testing.T) {
		manager := newFirstRunManager(t, withConfigHome(t))
		defer manager.Close()
		if !waitForEnabled(t, manager, closeTestWait) {
			t.Fatalf("armed loosening retry did not commit defaults within %v; the subject case below would pass vacuously", closeTestWait)
		}
	})

	t.Run("subject: Close stops it", func(t *testing.T) {
		manager := newFirstRunManager(t, withConfigHome(t))
		manager.Close()

		manager.mu.RLock()
		retry := manager.pendingLoosening.retry
		firstSeen := manager.pendingLoosening.firstSeen
		closed := manager.closed
		manager.mu.RUnlock()
		if retry != nil || !firstSeen.IsZero() {
			t.Fatalf("after Close the pending loosening = {retry: %v, firstSeen: %v}, want cleared", retry, firstSeen)
		}
		if !closed {
			t.Fatal("after Close the manager is not marked closed, so a later reload could still commit")
		}

		if waitForEnabled(t, manager, closeTestWait) {
			t.Fatalf("presence turned on %v after Close: the loosening retry was not stopped", closeTestWait)
		}
		select {
		case result := <-manager.Reloads():
			t.Fatalf("Close published a reload result %+v; shutdown must not resolve a pending loosening", result)
		default:
		}
	})
}

// TestManagerCloseIsRepeatableAndNeutralizesReload covers the two remaining
// contract points: Close is safe to call more than once, and a closed manager
// commits nothing in either direction, so shutdown cannot revert an already
// accepted config any more than it can enable presence.
func TestManagerCloseIsRepeatableAndNeutralizesReload(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, "enabled = true\nscan_interval = \"9s\"\n")
	manager := newManagerPathWithHorizon(path, snapshotConfigFile, time.Now, time.Sleep, closeTestHorizon)
	before, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() at construction = %v", err)
	}
	if !before.Enabled {
		t.Fatal("precondition not established: an explicit enabled = true config did not certify at construction")
	}

	manager.Close()
	manager.Close()

	// A reload after Close must not run: the file now says something else, and
	// a closed manager that still committed would be resolving state its owner
	// has already stopped caring about.
	writeConfig(t, path, "enabled = false\nscan_interval = \"31s\"\n")
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload() after Close = %v, want nil no-op", err)
	}
	after, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() after Close = %v", err)
	}
	if after.Enabled != before.Enabled || after.ScanInterval != before.ScanInterval {
		t.Fatalf("closed manager committed a state change: enabled %t -> %t, scan_interval %q -> %q",
			before.Enabled, after.Enabled, before.ScanInterval, after.ScanInterval)
	}
	select {
	case result := <-manager.Reloads():
		t.Fatalf("closed manager published %+v", result)
	default:
	}
}

// TestManagerCloseRacesFiringRetry aims Close at the exact moment the retry
// fires, which is the case time.Timer.Stop cannot win. Under -race this
// catches unsynchronized access; the assertion catches the subtler failure,
// which is a retry that lands after Close returned and changes state behind
// its owner's back.
func TestManagerCloseRacesFiringRetry(t *testing.T) {
	const managers = 16
	paths := make([]string, managers)
	for i := range paths {
		paths[i] = filepath.Join(t.TempDir(), defaultConfigFile)
	}
	var wg sync.WaitGroup
	for i := 0; i < managers; i++ {
		path := paths[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager := newManagerPathWithHorizon(
				path,
				snapshotConfigFile,
				time.Now,
				time.Sleep,
				closeTestHorizon,
			)
			// Land inside the window where the timer is about to fire or has
			// just fired, so both sides of the Stop race get exercised.
			time.Sleep(closeTestHorizon)
			manager.Close()

			// Whatever the retry did before Close, the state Close returns on
			// is final: nothing may move afterwards.
			settled, err := manager.Current()
			if err != nil {
				t.Errorf("Current() right after Close = %v", err)
				return
			}
			time.Sleep(4 * closeTestHorizon)
			later, err := manager.Current()
			if err != nil {
				t.Errorf("Current() after Close = %v", err)
				return
			}
			if later.Enabled != settled.Enabled {
				t.Errorf("config changed after Close returned: enabled %t -> %t", settled.Enabled, later.Enabled)
			}
		}()
	}
	wg.Wait()
}
