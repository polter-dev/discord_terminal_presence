//go:build unix

package detector

import (
	"os"
	"testing"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
)

// TestUnixOwnerResolverOwnsSelf proves the real gopsutil-backed resolver
// affirmatively recognizes the current process as belonging to the current
// effective user - the "normal detection still works" case for the actual
// OS-level comparison (Selector-level coverage is in detector_test.go).
func TestUnixOwnerResolverOwnsSelf(t *testing.T) {
	resolver := newSystemOwnerResolver()
	owned, err := resolver.Owned(int32(os.Getpid()), time.Time{})
	if err != nil {
		t.Fatalf("Owned(self) error = %v, want nil", err)
	}
	if !owned {
		t.Fatal("Owned(self) = false, want true: a process must always own itself")
	}
}

// TestUnixOwnerResolverVerifiesCreateTime reproduces the #569 mitigation:
// when a caller supplies the creation time it captured at identity time, the
// resolver must reject a pid whose current creation time does not match,
// rather than reporting ownership for whatever process now holds that pid.
func TestUnixOwnerResolverVerifiesCreateTime(t *testing.T) {
	resolver := newSystemOwnerResolver()
	pid := int32(os.Getpid())

	proc, err := psprocess.NewProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	millis, err := proc.CreateTime()
	if err != nil {
		t.Fatal(err)
	}
	actual := time.UnixMilli(millis)

	if owned, err := resolver.Owned(pid, actual); err != nil || !owned {
		t.Fatalf("Owned(self, actual createTime) = (%v, %v), want (true, nil)", owned, err)
	}

	mismatched := actual.Add(time.Hour)
	owned, err := resolver.Owned(pid, mismatched)
	if err == nil {
		t.Fatalf("Owned(self, mismatched createTime) = (%v, nil), want a non-nil error", owned)
	}
	if owned {
		t.Fatal("Owned(self, mismatched createTime) = true alongside an error; callers must never treat this as owned")
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
	owned, err := resolver.Owned(implausiblePID, time.Time{})
	if err == nil {
		t.Fatalf("Owned(nonexistent pid) = (%v, nil), want a non-nil error", owned)
	}
	if owned {
		t.Fatal("Owned(nonexistent pid) = true alongside an error; callers must never treat this as owned")
	}
}
