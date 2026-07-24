package main

import (
	"testing"
)

func TestShutdownEventNameDeterministicAndPIDSpecific(t *testing.T) {
	first := shutdownEventName(1234)
	second := shutdownEventName(1234)
	other := shutdownEventName(5678)

	if first != `Local\TermpDaemonShutdown-1234` {
		t.Fatalf("shutdownEventName(1234) = %q", first)
	}
	if first != second {
		t.Fatalf("shutdownEventName is not deterministic: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("shutdownEventName is not PID-specific: %q == %q", first, other)
	}
}

func TestRunShutdownSignalWaitCancelsWhenSignaled(t *testing.T) {
	canceled := false
	runShutdownSignalWait(func() (shutdownWaitResult, error) {
		return shutdownWaitCancel, nil
	}, func() {
		canceled = true
	})

	if !canceled {
		t.Fatal("cancel was not called")
	}
}

func TestRunShutdownSignalWaitDoesNotCancelWhenStopped(t *testing.T) {
	canceled := false
	runShutdownSignalWait(func() (shutdownWaitResult, error) {
		return shutdownWaitStop, nil
	}, func() {
		canceled = true
	})

	if canceled {
		t.Fatal("cancel was called for a stop result")
	}
}
