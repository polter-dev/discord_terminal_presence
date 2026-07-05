package detector

import (
	"context"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

type fakeLister struct {
	snapshots [][]Process
	index     int
}

func (f *fakeLister) List() ([]Process, error) {
	if len(f.snapshots) == 0 {
		return nil, nil
	}
	idx := f.index
	if idx >= len(f.snapshots) {
		idx = len(f.snapshots) - 1
	}
	f.index++
	return f.snapshots[idx], nil
}

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()

	reg, err := registry.New(
		registry.Tool{
			ID:          "tie-low",
			DisplayName: "Tie Low",
			Match:       registry.MatchSpec{Name: "tie-low"},
			Priority:    1,
		},
		registry.Tool{
			ID:          "tie-high",
			DisplayName: "Tie High",
			Match:       registry.MatchSpec{Name: "tie-high"},
			Priority:    10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.now = f.now.Add(d)
}

func TestActiveDetectionPicksMostRecentlyStarted(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	detection := ActiveDetection(testRegistry(t), []Process{
		{Name: "claude", CreateTime: base, Cwd: "/old"},
		{Name: "codex", CreateTime: base.Add(time.Minute), Cwd: "/new"},
	})

	if detection.None {
		t.Fatal("expected active detection")
	}
	if detection.Tool.ID != "codex-cli" {
		t.Fatalf("tool ID = %q, want codex-cli", detection.Tool.ID)
	}
	if detection.Cwd != "/new" {
		t.Fatalf("cwd = %q, want /new", detection.Cwd)
	}
}

func TestSelectorPinOverridesHeadliner(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		Pin:               "claude-code",
		ActivitySwitching: true,
	}, clock)

	detection := selector.Select([]Process{
		{Name: "claude", CreateTime: base, Cwd: "/claude", CPUTime: 1},
		{Name: "codex", CreateTime: base.Add(time.Minute), Cwd: "/codex", CPUTime: 100},
	})

	if detection.Tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want pinned claude-code", detection.Tool.ID)
	}
	if detection.Cwd != "/claude" {
		t.Fatalf("cwd = %q, want pinned cwd", detection.Cwd)
	}
}

func TestSelectorStickyKeepsPreviousHeadliner(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{ActivitySwitching: true}, clock)

	first := selector.Select([]Process{
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 1},
		{Name: "claude", CreateTime: base, CPUTime: 1},
	})
	if first.Tool.ID != "codex-cli" {
		t.Fatalf("first tool = %q, want codex-cli", first.Tool.ID)
	}

	clock.Advance(time.Second)
	next := selector.Select([]Process{
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 1},
		{Name: "claude", CreateTime: base, CPUTime: 1.5},
	})
	if next.Tool.ID != "codex-cli" {
		t.Fatalf("sticky tool = %q, want codex-cli", next.Tool.ID)
	}
}

func TestSelectorSwitchesAfterIdleTimeoutToActiveChallenger(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		HeadlinerIdleTimeout: 30 * time.Second,
		ActivitySwitching:    true,
	}, clock)

	first := selector.Select([]Process{
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 10},
		{Name: "claude", CreateTime: base, CPUTime: 1},
	})
	if first.Tool.ID != "codex-cli" {
		t.Fatalf("first tool = %q, want codex-cli", first.Tool.ID)
	}

	clock.Advance(time.Second)
	sticky := selector.Select([]Process{
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 10},
		{Name: "claude", CreateTime: base, CPUTime: 2},
	})
	if sticky.Tool.ID != "codex-cli" {
		t.Fatalf("tool before timeout = %q, want codex-cli", sticky.Tool.ID)
	}

	clock.Advance(31 * time.Second)
	switched := selector.Select([]Process{
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 10},
		{Name: "claude", CreateTime: base, CPUTime: 4},
	})
	if switched.Tool.ID != "claude-code" {
		t.Fatalf("tool after timeout = %q, want claude-code", switched.Tool.ID)
	}
}

func TestSelectorOrdersOthersByActivityThenPriority(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		Pin:               "claude-code",
		ActivitySwitching: true,
	}, clock)

	detection := selector.Select([]Process{
		{Name: "claude", CreateTime: base, CPUTime: 1},
		{Name: "nvim", CreateTime: base, CPUTime: 2},
		{Name: "lazygit", CreateTime: base, CPUTime: 2},
		{Name: "htop", CreateTime: base, CPUTime: 5},
	})

	got := []string{}
	for _, tool := range detection.Others {
		got = append(got, tool.ID)
	}
	want := []string{"htop", "lazygit", "nvim"}
	if len(got) != len(want) {
		t.Fatalf("others = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("others = %#v, want %#v", got, want)
		}
	}
}

func TestActiveDetectionUsesPriorityOnCreateTimeTie(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	detection := ActiveDetection(testRegistry(t), []Process{
		{Name: "tie-low", CreateTime: started},
		{Name: "tie-high", CreateTime: started},
	})

	if detection.Tool.ID != "tie-high" {
		t.Fatalf("tool ID = %q, want tie-high", detection.Tool.ID)
	}
}

func TestActiveDetectionDedupesToolInstances(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	detection := ActiveDetection(testRegistry(t), []Process{
		{Pid: 1, Name: "claude", CreateTime: base, Cwd: "/old"},
		{Pid: 2, Name: "claude", CreateTime: base.Add(time.Minute), Cwd: "/new"},
		{Name: "htop", CreateTime: base.Add(-time.Minute)},
	})

	if detection.Tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", detection.Tool.ID)
	}
	if detection.Cwd != "/new" {
		t.Fatalf("cwd = %q, want /new", detection.Cwd)
	}
}

func TestActiveDetectionMatchesClaudeVersionBinaryAndDedupes(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	detection := ActiveDetection(testRegistry(t), []Process{
		{
			Pid:        1,
			Name:       "2.1.201",
			Exe:        "/home/u/.local/share/claude/versions/2.1.201",
			Cmdline:    "claude --dangerously-skip-permissions",
			CreateTime: base,
			Cwd:        "/old",
		},
		{
			Pid:        2,
			Name:       "2.1.201",
			Exe:        "/home/u/.local/share/claude/versions/2.1.201",
			Cmdline:    "2.1.201 --worker",
			CreateTime: base.Add(time.Minute),
			Cwd:        "/new",
		},
	})

	if detection.None {
		t.Fatal("expected active detection")
	}
	if detection.Tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", detection.Tool.ID)
	}
	if detection.Cwd != "/new" {
		t.Fatalf("cwd = %q, want /new", detection.Cwd)
	}
}

func TestActiveDetectionReturnsNoneWhenNothingMatches(t *testing.T) {
	detection := ActiveDetection(testRegistry(t), []Process{
		{Name: "bash", CreateTime: time.Now()},
	})

	if !detection.None {
		t.Fatalf("expected none detection, got %#v", detection)
	}
}

func TestRunDebouncesBeforeEmitting(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lister := &fakeLister{snapshots: [][]Process{
		{{Name: "claude", CreateTime: base}},
		{{Name: "claude", CreateTime: base}},
	}}
	det, err := New(testRegistry(t), lister, Config{
		ScanInterval:   time.Millisecond,
		DebounceCycles: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := det.Run(ctx)

	select {
	case detection := <-ch:
		if detection.Tool.ID != "claude-code" {
			t.Fatalf("tool ID = %q, want claude-code", detection.Tool.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounced detection")
	}
}

func TestRunEmitsNoneAfterDebounce(t *testing.T) {
	lister := &fakeLister{snapshots: [][]Process{
		{{Name: "bash"}},
		{{Name: "bash"}},
	}}
	det, err := New(testRegistry(t), lister, Config{
		ScanInterval:   time.Millisecond,
		DebounceCycles: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := det.Run(ctx)

	select {
	case detection := <-ch:
		if !detection.None {
			t.Fatalf("expected none detection, got %#v", detection)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for none detection")
	}
}
