package detector

import (
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// countMatcherCalls replaces the matcher seam for the test and returns the
// per-process-name call counts, reset by the returned function.
func countMatcherCalls(t *testing.T) (map[string]int, func()) {
	t.Helper()
	calls := make(map[string]int)
	original := matchProcess
	matchProcess = func(reg *registry.Registry, info registry.ProcessInfo) (registry.Tool, bool) {
		calls[info.Name]++
		return original(reg, info)
	}
	t.Cleanup(func() { matchProcess = original })
	return calls, func() { clear(calls) }
}

func TestSelectorSkipsMatcherForUnchangedUnmatchedProcesses(t *testing.T) {
	base := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	reg := testRegistry(t)
	selector := NewSelector(reg, Config{ActivitySwitching: true}, &fakeClock{now: base})
	calls, reset := countMatcherCalls(t)

	processes := []Process{
		{Owned: true, Pid: 1, Name: "claude", Argv: []string{"claude"}, CreateTime: base},
		{Owned: true, Pid: 2, Name: "launchd-helper", Exe: "/usr/libexec/launchd-helper", Argv: []string{"/usr/libexec/launchd-helper", "--serve"}, CreateTime: base},
		{Owned: true, Pid: 3, Name: "editor", Cmdline: "editor notes.txt", CreateTime: base},
	}
	if detection := selector.Select(processes); detection.Tool.ID != "claude-code" {
		t.Fatalf("first scan tool = %q, want claude-code", detection.Tool.ID)
	}
	if calls["launchd-helper"] != 1 || calls["editor"] != 1 || calls["claude"] != 1 {
		t.Fatalf("first scan matcher calls = %v, want one per process", calls)
	}

	reset()
	if detection := selector.Select(processes); detection.Tool.ID != "claude-code" {
		t.Fatalf("second scan tool = %q, want claude-code", detection.Tool.ID)
	}
	if calls["launchd-helper"] != 0 || calls["editor"] != 0 {
		t.Fatalf("second scan matcher calls for unchanged unmatched processes = %v, want 0", calls)
	}
	if calls["claude"] != 1 {
		t.Fatalf("second scan matcher calls for matched process = %d, want 1", calls["claude"])
	}

	reset()
	changed := append([]Process(nil), processes...)
	changed[1].Argv = []string{"/usr/libexec/launchd-helper", "--other"}
	selector.Select(changed)
	if calls["launchd-helper"] != 1 || calls["editor"] != 0 {
		t.Fatalf("matcher calls after one identity changed = %v, want launchd-helper 1, editor 0", calls)
	}

	reset()
	selector.Reconfigure(reg, Config{ActivitySwitching: true})
	selector.Select(changed)
	if calls["launchd-helper"] != 1 || calls["editor"] != 1 {
		t.Fatalf("matcher calls after Reconfigure = %v, want every process rematched", calls)
	}
}
