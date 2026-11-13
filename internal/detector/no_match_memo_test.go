package detector

import (
	"reflect"
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

// TestSelectorNoMatchMemoMatchesUnmemoizedDetection runs one scan sequence
// through a selector with the memo and one with it cleared before every scan.
// The sequence includes a process that flips between matched and excluded by
// its subcommand, and a registry reload that makes an unmatched process match.
func TestSelectorNoMatchMemoMatchesUnmemoizedDetection(t *testing.T) {
	base := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	reg := testRegistry(t)
	reloaded, err := registry.New(registry.Tool{
		ID:          "late-tool",
		DisplayName: "Late Tool",
		Match:       registry.MatchSpec{Name: "late-tool"},
		Priority:    100,
	})
	if err != nil {
		t.Fatal(err)
	}

	claude := Process{Owned: true, Pid: 1, Name: "claude", Argv: []string{"claude"}, CreateTime: base}
	claudeDaemon := claude
	claudeDaemon.Argv = []string{"claude", "daemon"}
	codex := Process{Owned: true, Pid: 2, Name: "codex", Argv: []string{"codex"}, CreateTime: base, CPUTime: 1, CPUTimeKnown: true}
	late := Process{Owned: true, Pid: 3, Name: "late-tool", Argv: []string{"late-tool"}, CreateTime: base}
	other := Process{Owned: true, Pid: 4, Name: "unrelated", Argv: []string{"unrelated", "a", "b", "c"}, CreateTime: base}
	otherMoreArgs := other
	otherMoreArgs.Argv = []string{"unrelated", "a", "b", "c", "d"}

	scans := []struct {
		reload    *registry.Registry
		processes []Process
	}{
		{processes: []Process{claude, late, other}},
		{processes: []Process{claude, late, other}},
		{processes: []Process{claudeDaemon, late, otherMoreArgs}},
		{processes: []Process{claudeDaemon, codex, late, other}},
		{processes: []Process{claude, codex, late, other}},
		{reload: reloaded, processes: []Process{claude, codex, late, other}},
		{processes: []Process{claude, codex, late, other}},
		{reload: reg, processes: []Process{claudeDaemon, codex, late, other}},
		{processes: []Process{claude, late, other}},
	}

	clockOn := &fakeClock{now: base}
	clockOff := &fakeClock{now: base}
	config := Config{ActivitySwitching: true}
	on := NewSelector(reg, config, clockOn)
	off := NewSelector(reg, config, clockOff)
	sawLateTool := false
	for i, scan := range scans {
		if scan.reload != nil {
			on.Reconfigure(scan.reload, config)
			off.Reconfigure(scan.reload, config)
		}
		off.noMatch = nil
		got := on.Select(scan.processes)
		want := off.Select(scan.processes)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("scan %d: memo detection = %+v, unmemoized = %+v", i, got, want)
		}
		for _, tool := range append([]registry.Tool{got.Tool}, got.Others...) {
			sawLateTool = sawLateTool || tool.ID == "late-tool"
		}
		clockOn.Advance(DefaultScanInterval)
		clockOff.Advance(DefaultScanInterval)
	}
	if !sawLateTool {
		t.Fatal("late-tool was never detected after the registry reload")
	}
}
