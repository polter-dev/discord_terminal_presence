//go:build darwin

package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDarwinIdentityStartTimeEncoding pins the sec/usec -> uint64 encoding
// darwinIdentityStartTime uses so it stays comparable against
// processStartTime's own encoding (issue #560).
func TestDarwinIdentityStartTimeEncoding(t *testing.T) {
	identity := darwinProcessIdentity{startSec: 10, startUsec: 20}
	if got, want := darwinIdentityStartTime(identity), uint64(10)*1_000_000+20; got != want {
		t.Fatalf("darwinIdentityStartTime() = %d, want %d", got, want)
	}
}

// TestSignalTermpProcessAtPathBindsRecordedStartTime reproduces issue #560
// item 2 against a real spawned process: signalTermpProcessAtPath must
// refuse to signal when the caller's recorded start time does not match the
// process it is about to open a fresh identity snapshot for, and must
// succeed once the recorded start time matches. Before the fix,
// signalTermpProcessAtPath ignored the recorded start time entirely and
// signaled on owner+path alone.
func TestSignalTermpProcessAtPathBindsRecordedStartTime(t *testing.T) {
	if os.Getenv("TERMP_SIGNAL_START_TIME_HELPER") == "1" {
		_, _ = os.Stdout.WriteString("ready\n")
		_ = os.Stdout.Sync()
		time.Sleep(time.Minute)
		return
	}

	executable, err := currentProcessExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSignalTermpProcessAtPathBindsRecordedStartTime$")
	cmd.Env = append(os.Environ(), "TERMP_SIGNAL_START_TIME_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if scanner := bufio.NewScanner(stdout); !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("helper did not become ready: %q, %v", scanner.Text(), scanner.Err())
	}

	pid := cmd.Process.Pid
	actualStartTime, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime() error = %v", err)
	}

	// A recorded start time that does not match the live process (as after a
	// PID-reuse race) must refuse to signal it.
	if err := signalTermpProcessAtPath(pid, executable, actualStartTime+1, true); err == nil ||
		!strings.Contains(err.Error(), "process start time does not match recorded termp daemon") {
		t.Fatalf("signalTermpProcessAtPath() error = %v, want start-time mismatch refusal", err)
	}
	if !processAlive(pid) {
		t.Fatal("helper process exited after a refused mismatched-start-time signal")
	}

	// The recorded start time matches the live process: the signal proceeds.
	if err := signalTermpProcessAtPath(pid, executable, actualStartTime, true); err != nil {
		t.Fatalf("signalTermpProcessAtPath() error = %v, want success with matching start time", err)
	}
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if waitErr == nil || !errors.As(waitErr, &exitErr) {
		t.Fatalf("helper wait error = %v, want a termination exit error", waitErr)
	}
}
