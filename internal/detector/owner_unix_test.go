//go:build unix

package detector

import (
	"os"
	"testing"
)

// TestUnixOwnerResolverOwnsSelf proves the real gopsutil-backed resolver
// affirmatively recognizes the current process as belonging to the current
// effective user - the "normal detection still works" case for the actual
// OS-level comparison (Selector-level coverage is in detector_test.go).
func TestUnixOwnerResolverOwnsSelf(t *testing.T) {
	resolver := newSystemOwnerResolver()
	owned, err := resolver.Owned(int32(os.Getpid()))
	if err != nil {
		t.Fatalf("Owned(self) error = %v, want nil", err)
	}
	if !owned {
		t.Fatal("Owned(self) = false, want true: a process must always own itself")
	}
}

// TestUnixOwnerResolverFailsClosedOnMissingProcess proves the resolver
// surfaces an error - rather than silently reporting ownership - when a pid
// cannot be inspected (here, because it does not exist). The Selector-level
// contract (error => excluded) is proven separately in
// TestSelectorExcludesProcessWhenOwnerLookupFails; this test isolates that
// the resolver itself is the thing producing the error, not swallowing it.
func TestUnixOwnerResolverFailsClosedOnMissingProcess(t *testing.T) {
	resolver := newSystemOwnerResolver()
	// PIDs are 32-bit signed on these platforms; this value is far beyond any
	// real process table and is not reused within a test run.
	const implausiblePID = int32(1 << 30)
	owned, err := resolver.Owned(implausiblePID)
	if err == nil {
		t.Fatalf("Owned(nonexistent pid) = (%v, nil), want a non-nil error", owned)
	}
	if owned {
		t.Fatal("Owned(nonexistent pid) = true alongside an error; callers must never treat this as owned")
	}
}
