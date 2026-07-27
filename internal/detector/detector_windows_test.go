//go:build windows

package detector

import (
	"testing"
	"time"
)

func TestGopsutilListerRetainsWindowsAtimeStateAcrossScans(t *testing.T) {
	lister := NewGopsutilLister()
	first := lister.ttyAtimeSource()
	second := lister.ttyAtimeSource()
	if first != second {
		t.Fatal("GopsutilLister created a new Windows atime source between scans")
	}
}

func TestFirstUnfocusedObservationUsesPersistedEpisodeAtime(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	created := now.Add(-2 * time.Hour)
	lastAtime := now.Add(-time.Hour)
	episodes := NewEpisodeStore()
	episodes.Episodes[EpisodeKey("claude-code", 1, created)] = Episode{
		PresentSince: created,
		TTY:          "win:hwnd:100",
		LastAtime:    lastAtime,
	}
	path := t.TempDir() + `\presence.json`
	if err := SaveEpisodeStore(path, episodes); err != nil {
		t.Fatal(err)
	}
	episodes, err := LoadEpisodeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	enricher := &presenceProcessEnricher{
		resolver: fakeTTYResolver{resolutions: map[int32]TTYResolution{
			1: {Path: "win:hwnd:100"},
		}},
		tmux: fakeTmuxSnapshot{},
		atime: &windowsTTYAtimeSource{
			foregroundWindow: func() uintptr { return 200 },
			rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
			lastInputMillis:  func() (uint32, bool) { return 0, true },
			now:              func() time.Time { return now },
		},
		episodes: episodes,
	}
	selector := newSelectorWithEpisodes(testRegistry(t), Config{
		IdleClearTimeout:  20 * time.Minute,
		ActivitySwitching: true,
	}, &fakeClock{now: now}, episodes, nil)

	detection := selector.SelectWithEnricher([]Process{{
		Pid:        1,
		Name:       "claude",
		CreateTime: created,
	}}, enricher)

	if !detection.None {
		t.Fatalf("detection = %#v, want long-abandoned persisted episode to clear immediately", detection)
	}
}

func TestFirstUnfocusedObservationWithoutEpisodeFallsBackToNow(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	created := now.Add(-time.Minute)
	episodes := NewEpisodeStore()
	enricher := &presenceProcessEnricher{
		resolver: fakeTTYResolver{resolutions: map[int32]TTYResolution{
			1: {Path: "win:hwnd:100"},
		}},
		tmux: fakeTmuxSnapshot{},
		atime: &windowsTTYAtimeSource{
			foregroundWindow: func() uintptr { return 200 },
			rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
			lastInputMillis:  func() (uint32, bool) { return 0, true },
			now:              func() time.Time { return now },
		},
		episodes: episodes,
	}
	selector := newSelectorWithEpisodes(testRegistry(t), Config{
		IdleClearTimeout:  20 * time.Minute,
		ActivitySwitching: true,
	}, &fakeClock{now: now}, episodes, nil)

	detection := selector.SelectWithEnricher([]Process{{
		Pid:        1,
		Name:       "claude",
		CreateTime: created,
	}}, enricher)

	if detection.None || detection.Tool.ID != "claude-code" {
		t.Fatalf("detection = %#v, want new process to receive full grace period", detection)
	}
}

func TestSelectorIncludesToolInUnfocusedWindowAsOther(t *testing.T) {
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: base}
	selector := NewSelector(testRegistry(t), Config{
		IdleClearTimeout:       20 * time.Minute,
		CorroborateIdleWithCPU: true,
		ActivitySwitching:      true,
	}, clock)
	enricher := presenceEnricher(
		fakeTTYResolver{resolutions: map[int32]TTYResolution{
			1: {Path: "win:hwnd:100"},
			2: {Path: "win:hwnd:200"},
		}},
		fakeTmuxSnapshot{},
		&windowsTTYAtimeSource{
			foregroundWindow: func() uintptr { return 200 },
			rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
			lastInputMillis:  func() (uint32, bool) { return 0, true },
			now:              func() time.Time { return base },
		},
	)

	detection := selector.SelectWithEnricher([]Process{
		{Pid: 1, Name: "claude", CreateTime: base.Add(-time.Hour), CPUTime: 10, CPUTimeKnown: true},
		{Pid: 2, Name: "codex", CreateTime: base.Add(-time.Minute), CPUTime: 10, CPUTimeKnown: true},
	}, enricher)

	if detection.None || detection.Tool.ID != "codex-cli" {
		t.Fatalf("featured detection = %#v, want codex-cli", detection)
	}
	if len(detection.Others) != 1 || detection.Others[0].ID != "claude-code" {
		t.Fatalf("others = %#v, want claude-code from unfocused window", detection.Others)
	}
}
