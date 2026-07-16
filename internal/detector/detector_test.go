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

type fakeEnricher struct {
	processes map[int32]Process
	enriched  []int32
}

func newFakeEnricher(processes []Process) *fakeEnricher {
	enricher := &fakeEnricher{processes: make(map[int32]Process, len(processes))}
	for _, process := range processes {
		enricher.processes[process.Pid] = process
	}
	return enricher
}

func (f *fakeEnricher) Enrich(process Process) Process {
	f.enriched = append(f.enriched, process.Pid)
	if enriched, ok := f.processes[process.Pid]; ok {
		return enriched
	}
	return process
}

type channelLister struct {
	snapshots chan []Process
	last      []Process
}

func newChannelLister(snapshots ...[]Process) *channelLister {
	lister := &channelLister{snapshots: make(chan []Process, len(snapshots))}
	for _, snapshot := range snapshots {
		lister.snapshots <- append([]Process(nil), snapshot...)
	}
	close(lister.snapshots)
	return lister
}

func (f *channelLister) List() ([]Process, error) {
	snapshot, ok := <-f.snapshots
	if !ok {
		return append([]Process(nil), f.last...), nil
	}
	f.last = append([]Process(nil), snapshot...)
	return append([]Process(nil), snapshot...), nil
}

func testRegistry(t testing.TB) *registry.Registry {
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

func TestSelectorIdleClearAfterTwentyMinutesIdleAndActivityResetsWindow(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		IdleClearTimeout:  20 * time.Minute,
		ActivitySwitching: true,
	}, clock)

	processes := []Process{
		{Name: "claude", CreateTime: base, CPUTime: 0},
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 0},
	}
	first := selector.Select(processes)
	if first.None {
		t.Fatal("first idle sample should still display before idle_clear_timeout")
	}

	clock.Advance(19 * time.Minute)
	active := selector.Select([]Process{
		{Name: "claude", CreateTime: base, CPUTime: 0},
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 1},
	})
	if active.None {
		t.Fatal("activity inside idle_clear_timeout should keep detection")
	}

	clock.Advance(time.Second)
	processes[1].CPUTime = 1
	stillDisplayed := selector.Select(processes)
	if stillDisplayed.None {
		t.Fatal("first idle sample after activity should still display")
	}

	clock.Advance(20*time.Minute + time.Second)
	cleared := selector.Select(processes)
	if !cleared.None {
		t.Fatalf("expected idle clear none detection, got %#v", cleared)
	}

	clock.Advance(time.Second)
	resumed := selector.Select([]Process{
		{Name: "claude", CreateTime: base, CPUTime: 0},
		{Name: "codex", CreateTime: base.Add(time.Minute), CPUTime: 2},
	})
	if resumed.None {
		t.Fatal("expected activity to restore detection")
	}
	if resumed.Tool.ID != "codex-cli" {
		t.Fatalf("restored tool = %q, want codex-cli", resumed.Tool.ID)
	}
}

func TestSelectorIdleClearDisabledNeverClears(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		IdleClearTimeout:  0,
		ActivitySwitching: true,
	}, clock)

	processes := []Process{{Name: "claude", CreateTime: base, CPUTime: 0}}
	first := selector.Select(processes)
	if first.None {
		t.Fatal("expected detection while idle clear is disabled")
	}

	clock.Advance(24 * time.Hour)
	stillActive := selector.Select(processes)
	if stillActive.None {
		t.Fatal("idle_clear_timeout=0 should never clear a running known tool")
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

func TestSelectorClaudeHelpersDoNotChurnFeaturedInstance(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	selector := NewSelector(testRegistry(t), Config{ActivitySwitching: true}, &fakeClock{now: base})
	real := Process{
		Pid:        94948,
		Name:       "2.1.211",
		Exe:        "/Users/test/.local/share/claude/versions/2.1.211",
		Cmdline:    "claude -c --dangerously-skip-permissions",
		CreateTime: base,
		Cwd:        "/real-session",
	}

	first := selector.Select([]Process{
		real,
		{Pid: 94887, Name: "2.1.211", Cmdline: "claude bg-pty-host --bg-pty-host /tmp/cc-daemon-501/pty", CreateTime: base.Add(time.Minute), Cwd: "/helper"},
		{Pid: 94892, Name: "2.1.211", Cmdline: "claude bg-spare --bg-spare /tmp/cc-daemon-501/spare", CreateTime: base.Add(time.Minute), Cwd: "/helper"},
		{Pid: 95000, Name: "2.1.211", Cmdline: "claude daemon run --json-path /tmp/daemon.json", CreateTime: base.Add(2 * time.Minute), Cwd: "/helper"},
	})
	second := selector.Select([]Process{
		real,
		{Pid: 95009, Name: "2.1.211", Cmdline: "claude bg-pty-host --bg-pty-host /tmp/cc-daemon-501/pty", CreateTime: base.Add(3 * time.Minute), Cwd: "/respawned-helper"},
		{Pid: 95014, Name: "2.1.211", Cmdline: "claude bg-spare --bg-spare /tmp/cc-daemon-501/spare", CreateTime: base.Add(3 * time.Minute), Cwd: "/respawned-helper"},
	})

	for _, detection := range []Detection{first, second} {
		if detection.None {
			t.Fatal("expected interactive Claude session to remain detected")
		}
		if detection.Tool.ID != "claude-code" || detection.Cwd != real.Cwd || !detection.StartedAt.Equal(real.CreateTime) {
			t.Fatalf("featured = %#v, want real session cwd=%q started=%s", detection.Featured, real.Cwd, real.CreateTime)
		}
	}
}

func TestSelectWithEnricherMatchesFullSnapshotResults(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	full := []Process{
		{Pid: 1, Name: "bash", Cwd: "/ignored", CreateTime: base.Add(10 * time.Minute), CPUTime: 50},
		{Pid: 2, Name: "claude", Cwd: "/claude-old", CreateTime: base, CPUTime: 2},
		{Pid: 3, Name: "codex", Cwd: "/codex", CreateTime: base.Add(time.Minute), CPUTime: 3},
		{Pid: 4, Name: "nvim", Cwd: "/nvim", CreateTime: base.Add(-time.Minute), CPUTime: 4},
		{Pid: 5, Name: "claude", Cwd: "/claude-new", CreateTime: base.Add(2 * time.Minute), CPUTime: 5},
		{Pid: 6, Name: "zsh", Cwd: "/ignored-too", CreateTime: base.Add(20 * time.Minute), CPUTime: 60},
	}
	identities := make([]Process, 0, len(full))
	for _, process := range full {
		identities = append(identities, Process{
			Pid:     process.Pid,
			Name:    process.Name,
			Exe:     process.Exe,
			Cmdline: process.Cmdline,
			Argv0:   process.Argv0,
		})
	}

	reg := testRegistry(t)
	fullDetection := NewSelector(reg, Config{ActivitySwitching: true}, systemClock{}).Select(full)
	enricher := newFakeEnricher(full)
	lazyDetection := NewSelector(reg, Config{ActivitySwitching: true}, systemClock{}).
		SelectWithEnricher(identities, enricher)

	if !sameDetection(fullDetection, lazyDetection) {
		t.Fatalf("lazy detection = %#v, want %#v", lazyDetection, fullDetection)
	}
	wantEnriched := []int32{2, 3, 4, 5}
	if len(enricher.enriched) != len(wantEnriched) {
		t.Fatalf("enriched PIDs = %#v, want %#v", enricher.enriched, wantEnriched)
	}
	for i := range wantEnriched {
		if enricher.enriched[i] != wantEnriched[i] {
			t.Fatalf("enriched PIDs = %#v, want %#v", enricher.enriched, wantEnriched)
		}
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

func TestRunEmitsDebouncedSequenceAndClosesOnCancel(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lister := newChannelLister(
		[]Process{{Name: "claude", CreateTime: base, Cwd: "/claude"}},
		[]Process{{Name: "bash"}},
		[]Process{{Name: "claude", CreateTime: base, Cwd: "/claude"}},
		[]Process{{Name: "claude", CreateTime: base, Cwd: "/claude"}},
		[]Process{{Name: "codex", CreateTime: base.Add(time.Minute), Cwd: "/codex"}},
		[]Process{{Name: "codex", CreateTime: base.Add(time.Minute), Cwd: "/codex"}},
		[]Process{{Name: "bash"}},
		[]Process{{Name: "bash"}},
	)
	det, err := New(testRegistry(t), lister, Config{
		ScanInterval:   time.Nanosecond,
		DebounceCycles: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := det.Run(ctx)

	got := make([]Detection, 0, 3)
	for len(got) < 3 {
		select {
		case detection, ok := <-ch:
			if !ok {
				t.Fatalf("detection channel closed after %d emissions, want 3", len(got))
			}
			got = append(got, detection)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for detection %d", len(got)+1)
		}
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("detection channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for detector shutdown")
	}

	if got[0].Tool.ID != "claude-code" || got[0].Cwd != "/claude" {
		t.Fatalf("first detection = %#v, want claude-code /claude", got[0])
	}
	if got[1].Tool.ID != "codex-cli" || got[1].Cwd != "/codex" {
		t.Fatalf("second detection = %#v, want codex-cli /codex", got[1])
	}
	if !got[2].None {
		t.Fatalf("third detection = %#v, want none", got[2])
	}
}
