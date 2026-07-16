package detector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

type fakeTTYResolver struct {
	resolutions map[int32]TTYResolution
	errors      map[int32]error
}

func (f fakeTTYResolver) Resolve(pid int32) (TTYResolution, error) {
	return f.resolutions[pid], f.errors[pid]
}

type fakeTmuxSnapshot struct {
	detached map[string]bool
	known    map[string]bool
}

func (f fakeTmuxSnapshot) Detached(tty string) (bool, bool) {
	return f.detached[tty], f.known[tty]
}

type fakeAtimeSource struct {
	atimes map[string]time.Time
	errors map[string]error
}

func (f fakeAtimeSource) Atime(tty string) (time.Time, error) {
	return f.atimes[tty], f.errors[tty]
}

func presenceEnricher(resolver TTYResolver, tmux TmuxPaneSnapshot, atime TTYAtimeSource) ProcessEnricher {
	return newPresenceProcessEnricher(nil, resolver, tmux, atime)
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

func TestSelectorStaleAtimeExcludedFromCandidatesAndOthers(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		IdleClearTimeout:  20 * time.Minute,
		ActivitySwitching: true,
	}, clock)
	enricher := presenceEnricher(
		fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/ttys001"}, 2: {Path: "/dev/ttys002"}}},
		fakeTmuxSnapshot{},
		fakeAtimeSource{atimes: map[string]time.Time{"/dev/ttys001": base.Add(-21 * time.Minute), "/dev/ttys002": base}},
	)
	detection := selector.SelectWithEnricher([]Process{
		{Pid: 1, Name: "claude", CreateTime: base.Add(-time.Hour)},
		{Pid: 2, Name: "codex", CreateTime: base.Add(-time.Minute)},
	}, enricher)
	if detection.None || detection.Tool.ID != "codex-cli" || len(detection.Others) != 0 {
		t.Fatalf("detection = %#v, want only fresh codex", detection)
	}
	if staleOnly := selector.SelectWithEnricher([]Process{{Pid: 1, Name: "claude", CreateTime: base.Add(-time.Hour)}}, enricher); !staleOnly.None {
		t.Fatalf("stale-only detection = %#v, want none", staleOnly)
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

func TestSelectorFreshAndFutureAtimeArePresent(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		IdleClearTimeout:  20 * time.Minute,
		ActivitySwitching: true,
	}, clock)
	for name, atime := range map[string]time.Time{"fresh": base.Add(-time.Minute), "future": base.Add(time.Hour)} {
		t.Run(name, func(t *testing.T) {
			enricher := presenceEnricher(fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/ttys001"}}}, fakeTmuxSnapshot{}, fakeAtimeSource{atimes: map[string]time.Time{"/dev/ttys001": atime}})
			if detection := selector.SelectWithEnricher([]Process{{Pid: 1, Name: "claude", CreateTime: base.Add(-time.Hour)}}, enricher); detection.None {
				t.Fatalf("%s atime should remain present", name)
			}
		})
	}
}

func TestSelectorDetachedTmuxExcludedImmediately(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	selector := NewSelector(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, &fakeClock{now: base})
	enricher := presenceEnricher(fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/ttys001"}}}, fakeTmuxSnapshot{detached: map[string]bool{"/dev/ttys001": true}, known: map[string]bool{"/dev/ttys001": true}}, fakeAtimeSource{atimes: map[string]time.Time{"/dev/ttys001": base}})
	if detection := selector.SelectWithEnricher([]Process{{Pid: 1, Name: "claude", CreateTime: base}}, enricher); !detection.None {
		t.Fatalf("detached tmux detection = %#v, want none", detection)
	}
}

func TestSelectorTmuxUnknownFallsThroughToAtime(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, name := range []string{"query error", "missing tmux", "unmatched tty"} {
		t.Run(name, func(t *testing.T) {
			selector := NewSelector(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, &fakeClock{now: base})
			enricher := presenceEnricher(fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/ttys001"}}}, fakeTmuxSnapshot{}, fakeAtimeSource{atimes: map[string]time.Time{"/dev/ttys001": base}})
			if detection := selector.SelectWithEnricher([]Process{{Pid: 1, Name: "claude", CreateTime: base}}, enricher); detection.None {
				t.Fatal("unknown tmux state must fall through to fresh atime")
			}
		})
	}
}

func TestSelectorTTYResolutionFailureKeepsAndDefinitiveNoTTYExcludes(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	selector := NewSelector(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, &fakeClock{now: base})
	resolver := fakeTTYResolver{
		resolutions: map[int32]TTYResolution{2: {NoTTY: true}},
		errors:      map[int32]error{1: errors.New("sysctl denied")},
	}
	enricher := presenceEnricher(resolver, fakeTmuxSnapshot{}, fakeAtimeSource{})
	kept := selector.SelectWithEnricher([]Process{{Pid: 1, Name: "claude", CreateTime: base}}, enricher)
	if kept.None {
		t.Fatal("tty resolution failure must fail open")
	}
	excluded := selector.SelectWithEnricher([]Process{{Pid: 2, Name: "claude", CreateTime: base}}, enricher)
	if !excluded.None {
		t.Fatalf("definitive no-tty detection = %#v, want none", excluded)
	}
}

func TestSelectorAtimeStatFailureKeeps(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	selector := NewSelector(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, &fakeClock{now: base})
	enricher := presenceEnricher(
		fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/ttys001"}}},
		fakeTmuxSnapshot{},
		fakeAtimeSource{errors: map[string]error{"/dev/ttys001": errors.New("stat denied")}},
	)
	if detection := selector.SelectWithEnricher([]Process{{Pid: 1, Name: "claude", CreateTime: base}}, enricher); detection.None {
		t.Fatal("atime stat failure must fail open")
	}
}

func TestTmuxPaneSnapshotRequiresEveryOwningSessionDetached(t *testing.T) {
	panes, err := parseTmuxPanes("/dev/ttys001\t0\n/dev/ttys001\t1\n/dev/ttys002\t0\n")
	if err != nil {
		t.Fatal(err)
	}
	if detached, matched := panes.Detached("/dev/ttys001"); !matched || detached {
		t.Fatal("a pane linked into an attached session must be kept")
	}
	if detached, matched := panes.Detached("/dev/ttys002"); !matched || !detached {
		t.Fatal("a pane owned only by detached sessions must be excluded")
	}
	if _, err := parseTmuxPanes("malformed"); err == nil {
		t.Fatal("malformed tmux output must produce an unknown snapshot")
	}
}

func TestSelectorPresenceEpisodeAnchorsAndPIDReuse(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, clock)
	resolver := fakeTTYResolver{resolutions: map[int32]TTYResolution{7: {Path: "/dev/ttys007"}}}
	atimes := fakeAtimeSource{atimes: map[string]time.Time{"/dev/ttys007": base}}
	enricher := presenceEnricher(resolver, fakeTmuxSnapshot{}, atimes)
	process := Process{Pid: 7, Name: "claude", CreateTime: base.Add(-time.Hour)}

	first := selector.SelectWithEnricher([]Process{process}, enricher)
	if !first.StartedAt.Equal(base) {
		t.Fatalf("first anchor = %s, want %s", first.StartedAt, base)
	}
	clock.Advance(5 * time.Minute)
	continuous := selector.SelectWithEnricher([]Process{process}, enricher)
	if !continuous.StartedAt.Equal(base) {
		t.Fatalf("continuous anchor = %s, want %s", continuous.StartedAt, base)
	}

	selector.Select(nil)
	clock.Advance(time.Minute)
	afterAbsence := selector.SelectWithEnricher([]Process{process}, enricher)
	if !afterAbsence.StartedAt.Equal(clock.Now()) {
		t.Fatalf("post-absence anchor = %s, want %s", afterAbsence.StartedAt, clock.Now())
	}

	clock.Advance(time.Minute)
	reused := process
	reused.CreateTime = process.CreateTime.Add(30 * time.Minute)
	pidReuse := selector.SelectWithEnricher([]Process{reused}, enricher)
	if !pidReuse.StartedAt.Equal(clock.Now()) {
		t.Fatalf("PID-reuse anchor = %s, want %s", pidReuse.StartedAt, clock.Now())
	}
}

func TestEpisodePersistenceRoundTripAndCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "presence.json")
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := NewEpisodeStore()
	key := EpisodeKey("claude-code", 7, base.Add(-time.Hour))
	tty := TTYInfo{State: TTYResolved, Path: "/dev/ttys007", Atime: base, AtimeKnown: true}
	anchor, _ := store.Observe(key, tty, base, 20*time.Minute)
	if err := SaveEpisodeStore(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEpisodeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	resumed, _ := loaded.Observe(key, tty, base.Add(time.Minute), 20*time.Minute)
	if !resumed.Equal(anchor) {
		t.Fatalf("round-trip anchor = %s, want %s", resumed, anchor)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := LoadEpisodeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := corrupt.Observe(key, tty, base.Add(2*time.Minute), 20*time.Minute); !got.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("corrupt state anchor = %s, want a safe new episode", got)
	}
	selector := newSelectorWithEpisodes(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, &fakeClock{now: base.Add(2 * time.Minute)}, corrupt, nil)
	if detection := selector.Select([]Process{{Pid: 7, Name: "claude", CreateTime: base.Add(-time.Hour)}}); detection.None {
		t.Fatal("corrupt state must not exclude an otherwise uncertain process")
	}
}

func TestEpisodeRestartAfterAtimeGapStartsNewAnchor(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	key := EpisodeKey("claude-code", 7, base.Add(-time.Hour))
	store := NewEpisodeStore()
	oldTTY := TTYInfo{State: TTYResolved, Path: "/dev/ttys007", Atime: base, AtimeKnown: true}
	store.Observe(key, oldTTY, base, 20*time.Minute)
	path := filepath.Join(t.TempDir(), "presence.json")
	if err := SaveEpisodeStore(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadEpisodeStore(path)
	now := base.Add(time.Hour)
	newTTY := oldTTY
	newTTY.Atime = base.Add(21 * time.Minute)
	anchor, _ := loaded.Observe(key, newTTY, now, 20*time.Minute)
	if !anchor.Equal(now) {
		t.Fatalf("anchor after atime gap = %s, want %s", anchor, now)
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
		if detection.Tool.ID != "claude-code" || detection.Cwd != real.Cwd || !detection.StartedAt.Equal(base) {
			t.Fatalf("featured = %#v, want real session cwd=%q episode=%s", detection.Featured, real.Cwd, base)
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
	clock := &fakeClock{now: base.Add(time.Hour)}
	fullDetection := NewSelector(reg, Config{ActivitySwitching: true}, clock).Select(full)
	enricher := newFakeEnricher(full)
	lazyDetection := NewSelector(reg, Config{ActivitySwitching: true}, clock).
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
	det.presenceStatePath = filepath.Join(t.TempDir(), "presence.json")

	ctx, cancel := context.WithCancel(context.Background())

	ch := det.Run(ctx)

	select {
	case detection := <-ch:
		if detection.Tool.ID != "claude-code" {
			t.Fatalf("tool ID = %q, want claude-code", detection.Tool.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounced detection")
	}
	cancel()
	for range ch {
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
	det.presenceStatePath = filepath.Join(t.TempDir(), "presence.json")

	ctx, cancel := context.WithCancel(context.Background())

	ch := det.Run(ctx)

	select {
	case detection := <-ch:
		if !detection.None {
			t.Fatalf("expected none detection, got %#v", detection)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for none detection")
	}
	cancel()
	for range ch {
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
	det.presenceStatePath = filepath.Join(t.TempDir(), "presence.json")

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
