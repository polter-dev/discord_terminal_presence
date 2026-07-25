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
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, func(string) (int, error) {
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

func TestWaitForDetachedStartReportsChildExit(t *testing.T) {
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, func(string) (int, error) {
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

func TestWaitForDetachedStartReportsOtherPIDFileOwner(t *testing.T) {
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, func(string) (int, error) {
		return 5678, nil
	}, func(int) bool {
		t.Fatal("checked child after finding another owner")
		return false
	}, func(int) bool {
		t.Fatal("checked process identity after finding another owner")
		return false
	}, func(time.Duration) {
		t.Fatal("slept after finding another owner")
	})
	if err == nil || !strings.Contains(err.Error(), "owned by pid 5678 instead of spawned pid 1234") {
		t.Fatalf("waitForDetachedStart() error = %v, want actual owner", err)
	}
}

func TestWaitForDetachedStartTimesOutBoundedly(t *testing.T) {
	var slept time.Duration
	err := waitForDetachedStart("termp.pid", 1234, 60*time.Millisecond, 25*time.Millisecond, func(string) (int, error) {
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

func TestWaitForDetachedStartReportsPIDFileReadError(t *testing.T) {
	readErr := errors.New("permission denied")
	err := waitForDetachedStart("termp.pid", 1234, time.Second, 25*time.Millisecond, func(string) (int, error) {
		return 0, readErr
	}, func(int) bool {
		return true
	}, func(int) bool {
		return true
	}, func(time.Duration) {
		t.Fatal("slept after PID file read error")
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("waitForDetachedStart() error = %v, want wrapped read error", err)
	}
}
