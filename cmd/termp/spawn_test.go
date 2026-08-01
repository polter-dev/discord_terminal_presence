package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWaitForDetachedStartConfirmsPIDFileOwnerIsLive(t *testing.T) {
	reads := 0
	var slept time.Duration
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		reads++
		if reads < 3 {
			return 0, os.ErrNotExist
		}
		return 1234, nil
	}, func(pid int) bool {
		return pid == 1234
	}, func(pid int) bool {
		return pid == 1234
	}, func(delay time.Duration) {
		slept += delay
	})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 3 || slept != 50*time.Millisecond {
		t.Fatalf("readiness polling: reads=%d slept=%s; want 3, 50ms", reads, slept)
	}
}

func TestWaitForDetachedStartReportsDeathDuringStabilityWindow(t *testing.T) {
	// The child confirms PID-file ownership immediately, then dies partway
	// through the stability window (simulating run() erroring out of
	// initialization after the PID write, which removes the PID file via
	// start()'s deferred removePIDIfOwned) — issue #490. This must be
	// reported as a failed start, not a successful one.
	reads := 0
	aliveChecks := 0
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 120*time.Millisecond, func(string) (int, error) {
		reads++
		if reads >= 5 {
			// The daemon's deferred cleanup removed the PID file once it
			// died.
			return 0, os.ErrNotExist
		}
		return 1234, nil
	}, func(pid int) bool {
		aliveChecks++
		if aliveChecks >= 5 {
			return false
		}
		return pid == 1234
	}, func(pid int) bool {
		return pid == 1234
	}, func(time.Duration) {})
	if err == nil {
		t.Fatal("waitForDetachedStart() = nil, want death-during-stability-window error")
	}
	if !strings.Contains(err.Error(), "1234") {
		t.Fatalf("waitForDetachedStart() error = %v, want it to name pid 1234", err)
	}
}

func TestWaitForDetachedStartSucceedsPromptlyWhenChildStaysAlive(t *testing.T) {
	// A healthy child confirms ownership and then stays alive/owns the PID
	// file for the whole stability window: waitForDetachedStart must return
	// success, and the added latency must stay bounded to the stability
	// window rather than the full timeout budget.
	var slept time.Duration
	err := waitForDetachedStart("termp.pid", 1234, 2*time.Second, 25*time.Millisecond, 120*time.Millisecond, func(string) (int, error) {
		return 1234, nil
	}, func(pid int) bool {
		return pid == 1234
	}, func(pid int) bool {
		return pid == 1234
	}, func(delay time.Duration) {
		slept += delay
	})
	if err != nil {
		t.Fatalf("waitForDetachedStart() error = %v, want success for a healthy child", err)
	}
	if slept != 120*time.Millisecond {
		t.Fatalf("stability-window sleep = %s, want exactly the 120ms window (not the 2s timeout budget)", slept)
	}
}

func TestWaitForDetachedStartReportsChildExit(t *testing.T) {
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		return 0, os.ErrNotExist
	}, func(int) bool {
		return false
	}, func(int) bool {
		t.Fatal("checked process identity after child exit")
		return false
	}, func(time.Duration) {
		t.Fatal("slept after child exit")
	})
	if err == nil || !strings.Contains(err.Error(), "pid 1234 exited before owning the PID file") {
		t.Fatalf("waitForDetachedStart() error = %v, want child-exit error", err)
	}
}

func TestWaitForDetachedStartIgnoresDeadStaleOwner(t *testing.T) {
	reads := 0
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		reads++
		if reads == 1 {
			return 5678, nil
		}
		return 1234, nil
	}, func(pid int) bool {
		return pid == 1234
	}, func(pid int) bool {
		return pid == 1234
	}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("PID file reads = %d, want 2", reads)
	}
}

func TestWaitForDetachedStartIgnoresLiveNonTermpStaleOwner(t *testing.T) {
	reads := 0
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		reads++
		if reads == 1 {
			return 5678, nil
		}
		return 1234, nil
	}, func(pid int) bool {
		return pid == 1234 || pid == 5678
	}, func(pid int) bool {
		return pid == 1234
	}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("PID file reads = %d, want 2", reads)
	}
}

func TestWaitForDetachedStartReportsLiveCompetingTermpOwner(t *testing.T) {
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		return 5678, nil
	}, func(pid int) bool {
		return pid == 5678
	}, func(pid int) bool {
		return pid == 5678
	}, func(time.Duration) {
		t.Fatal("slept after finding live competing owner")
	})
	if err == nil || !strings.Contains(err.Error(), "owned by pid 5678 instead of spawned pid 1234") {
		t.Fatalf("waitForDetachedStart() error = %v, want actual owner", err)
	}
}

func TestWaitForDetachedStartStaleOwnerTimesOutBoundedly(t *testing.T) {
	var slept time.Duration
	err := waitForDetachedStart("termp.pid", 1234, 60*time.Millisecond, 25*time.Millisecond, 0, func(string) (int, error) {
		return 5678, nil
	}, func(pid int) bool {
		return pid == 1234
	}, func(int) bool {
		return false
	}, func(delay time.Duration) {
		slept += delay
	})
	if err == nil || !strings.Contains(err.Error(), "startup could not be confirmed within 60ms") {
		t.Fatalf("waitForDetachedStart() error = %v, want timeout", err)
	}
	if slept != 60*time.Millisecond {
		t.Fatalf("readiness wait slept %s, want bounded 60ms", slept)
	}
}

func TestWaitForDetachedStartTimesOutBoundedly(t *testing.T) {
	var slept time.Duration
	err := waitForDetachedStart("termp.pid", 1234, 60*time.Millisecond, 25*time.Millisecond, 0, func(string) (int, error) {
		return 0, os.ErrNotExist
	}, func(int) bool {
		return true
	}, func(int) bool {
		return false
	}, func(delay time.Duration) {
		slept += delay
	})
	if err == nil || !strings.Contains(err.Error(), "startup could not be confirmed within 60ms") {
		t.Fatalf("waitForDetachedStart() error = %v, want timeout", err)
	}
	if slept != 60*time.Millisecond {
		t.Fatalf("readiness wait slept %s, want bounded 60ms", slept)
	}
}

func TestWaitForDetachedStartRetriesPIDFileReadError(t *testing.T) {
	readErr := errors.New("permission denied")
	reads := 0
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		reads++
		if reads == 1 {
			return 0, readErr
		}
		return 1234, nil
	}, func(int) bool {
		return true
	}, func(int) bool {
		return true
	}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("PID file reads = %d, want 2", reads)
	}
}

func TestWaitForDetachedStartReadErrorTimesOutWithCause(t *testing.T) {
	readErr := errors.New("permission denied")
	err := waitForDetachedStart("termp.pid", 1234, 60*time.Millisecond, 25*time.Millisecond, 0, func(string) (int, error) {
		return 0, readErr
	}, func(int) bool {
		return true
	}, func(int) bool {
		return true
	}, func(time.Duration) {})
	if !errors.Is(err, readErr) || !strings.Contains(err.Error(), "startup could not be confirmed within 60ms") {
		t.Fatalf("waitForDetachedStart() error = %v, want timeout wrapping read error", err)
	}
}

func TestWaitForDetachedStartReadErrorPrefersChildExit(t *testing.T) {
	readErr := errors.New("permission denied")
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, 0, func(string) (int, error) {
		return 0, readErr
	}, func(int) bool {
		return false
	}, func(int) bool {
		t.Fatal("checked process identity after child exit")
		return false
	}, func(time.Duration) {
		t.Fatal("slept after child exit")
	})
	if err == nil || errors.Is(err, readErr) || !strings.Contains(err.Error(), "pid 1234 exited before owning the PID file") {
		t.Fatalf("waitForDetachedStart() error = %v, want child-exit error", err)
	}
}
