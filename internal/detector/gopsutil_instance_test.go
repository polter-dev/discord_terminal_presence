package detector

import (
	"os"
	"testing"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
)

// TestEnrichVerifyingInstanceDiscardsMismatchedCreateTime reproduces the
// gopsutil.go half of #569: enrichment opens a brand-new *psprocess.Process
// handle for the matched pid, independent of the handle identity was
// captured from. If that pid was recycled in between (the original process
// exited and a new one, e.g. one of the user's own tools, reused the pid),
// the fresh handle's CreateTime will not match what was captured at identity
// time. enrichVerifyingInstance must then discard the freshly read fields
// (cwd, cpu, create time) rather than mixing them with the identity
// (name/argv) captured from the now-exited original process.
func TestEnrichVerifyingInstanceDiscardsMismatchedCreateTime(t *testing.T) {
	proc, err := psprocess.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}

	identityCreateTime := time.Now().Add(-time.Hour) // deliberately does not match the real process
	identity := Process{Pid: int32(os.Getpid()), Name: "claude", CreateTime: identityCreateTime}

	got := enrichVerifyingInstance(proc, identity)
	if !got.CreateTime.Equal(identityCreateTime) {
		t.Fatalf("CreateTime = %v, want the original identity CreateTime %v preserved on mismatch", got.CreateTime, identityCreateTime)
	}
	if got.Cwd != "" {
		t.Fatalf("Cwd = %q, want empty: enrichment fields must be discarded on a CreateTime mismatch", got.Cwd)
	}
	if got.Name != "claude" {
		t.Fatalf("Name = %q, want the original identity preserved", got.Name)
	}
}

// TestEnrichVerifyingInstanceAcceptsMatchingCreateTime is the companion
// positive case: when the pid was not recycled, enrichment must still apply
// normally.
func TestEnrichVerifyingInstanceAcceptsMatchingCreateTime(t *testing.T) {
	proc, err := psprocess.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	millis, err := proc.CreateTime()
	if err != nil {
		t.Fatal(err)
	}
	actual := time.UnixMilli(millis)

	identity := Process{Pid: int32(os.Getpid()), Name: "claude", CreateTime: actual}
	got := enrichVerifyingInstance(proc, identity)
	if !got.CreateTime.Equal(actual) {
		t.Fatalf("CreateTime = %v, want %v", got.CreateTime, actual)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd != wd {
		t.Fatalf("Cwd = %q, want %q: enrichment must still apply when CreateTime matches", got.Cwd, wd)
	}
}
