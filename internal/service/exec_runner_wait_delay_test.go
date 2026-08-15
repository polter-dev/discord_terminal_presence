package service

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestExecRunnerRunContextEnforcesWaitDelay covers #558: exec.CommandContext's
// cancel func only kills the direct child. CombinedOutput blocks in Wait()
// until every writer to the child's inherited stdout/stderr pipe closes, not
// until the child itself dies, so a grandchild that outlives the direct
// child and keeps holding that pipe open kept RunContext blocked long past
// its context deadline. This spawns exactly that shape (a detached
// grandchild that outlives its parent while inheriting the pipe) under a
// short context and asserts RunContext returns close to the deadline instead
// of after the grandchild's own sleep.
func TestExecRunnerRunContextEnforcesWaitDelay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell to fork a lingering grandchild")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := ExecRunner{}.RunContext(ctx, "sh", "-c", "sleep 3 & exec sleep 10")
	elapsed := time.Since(start)

	if elapsed > 2700*time.Millisecond {
		t.Fatalf("RunContext returned after %s (context bound 200ms), err = %v; want it bounded by WaitDelay instead of the grandchild's 3s sleep", elapsed, err)
	}
	if err == nil {
		t.Fatal("RunContext() error = nil, want a timeout-shaped error")
	}
}

// TestExecRunnerRunContextReturnsContextErrOnTimeout covers the secondary
// effect of #558: a killed command after the context is done used to surface
// as the child's own "signal: killed" instead of ctx.Err(), so callers could
// not distinguish a timeout from a genuine command failure. That distinction
// matters because #556's Windows ownership check coerced that ambiguous
// error into "installed and owned."
func TestExecRunnerRunContextReturnsContextErrOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ExecRunner{}.RunContext(ctx, "sleep", "5")
	if err != context.DeadlineExceeded {
		t.Fatalf("RunContext() error = %v, want %v", err, context.DeadlineExceeded)
	}
}
