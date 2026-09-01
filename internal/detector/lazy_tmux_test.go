package detector

import "testing"

func TestScanWithoutMatchedToolsDoesNotQueryTmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	queryCalls := 0
	enricher := NewGopsutilLister().NewScanProcessEnricher()
	presence, ok := enricher.(*presenceProcessEnricher)
	if !ok {
		t.Fatalf("scan enricher type = %T, want *presenceProcessEnricher", enricher)
	}
	tmux, ok := presence.tmux.(*lazyTmuxPanes)
	if !ok {
		t.Fatalf("scan tmux snapshot type = %T, want *lazyTmuxPanes", presence.tmux)
	}
	tmux.query = func() TmuxPaneSnapshot {
		queryCalls++
		return fakeTmuxSnapshot{}
	}
	selector := NewSelector(testRegistry(t), Config{}, &fakeClock{})

	detection := selector.SelectWithEnricher([]Process{{Name: "not-a-known-tool-596"}}, enricher)
	if !detection.None {
		t.Fatalf("zero-match scan detection = %#v, want none", detection)
	}
	if queryCalls != 0 {
		t.Fatalf("zero-match scan queried tmux %d times, want 0", queryCalls)
	}
}

func TestScanQueriesTmuxOnceAcrossMultipleDetachedCalls(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	queryCalls := 0
	detachedCalls := 0
	enricher := NewGopsutilLister().NewScanProcessEnricher()
	presence, ok := enricher.(*presenceProcessEnricher)
	if !ok {
		t.Fatalf("scan enricher type = %T, want *presenceProcessEnricher", enricher)
	}
	tmux, ok := presence.tmux.(*lazyTmuxPanes)
	if !ok {
		t.Fatalf("scan tmux snapshot type = %T, want *lazyTmuxPanes", presence.tmux)
	}
	tmux.query = func() TmuxPaneSnapshot {
		queryCalls++
		return countingTmuxSnapshot{calls: &detachedCalls}
	}
	presence.base = nil
	presence.resolver = fakeTTYResolver{resolutions: map[int32]TTYResolution{
		1: {Path: "/dev/pts/1"},
		2: {Path: "/dev/pts/2"},
	}}
	presence.atime = nil
	presence.owner = fakeOwnerResolver{}
	selector := NewSelector(testRegistry(t), Config{}, &fakeClock{})

	detection := selector.SelectWithEnricher([]Process{
		{Pid: 1, Name: "tie-low"},
		{Pid: 2, Name: "tie-low"},
	}, enricher)
	if detection.None {
		t.Fatal("matched scan detection = none, want a present tool")
	}
	if queryCalls != 1 {
		t.Fatalf("matched scan queried tmux %d times, want 1", queryCalls)
	}
	if detachedCalls != 2 {
		t.Fatalf("matched scan read the tmux snapshot %d times, want 2", detachedCalls)
	}
}

func TestLazyTmuxSnapshotIsScopedToOneScan(t *testing.T) {
	queryCalls := 0
	query := func() TmuxPaneSnapshot {
		queryCalls++
		return fakeTmuxSnapshot{}
	}
	firstScan := &lazyTmuxPanes{query: query}
	secondScan := &lazyTmuxPanes{query: query}

	firstScan.Detached("/dev/pts/1")
	firstScan.Detached("/dev/pts/2")
	secondScan.Detached("/dev/pts/1")
	if queryCalls != 2 {
		t.Fatalf("two scan snapshots queried tmux %d times, want 2", queryCalls)
	}
}

func TestLazyTmuxSnapshotPreservesMissingTmuxBehavior(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	eager := queryTmuxPanes()
	lazy := &lazyTmuxPanes{query: queryTmuxPanes}

	wantDetached, wantMatched := eager.Detached("/dev/pts/1")
	gotDetached, gotMatched := lazy.Detached("/dev/pts/1")
	if gotDetached != wantDetached || gotMatched != wantMatched {
		t.Fatalf("lazy missing-tmux result = (%t, %t), eager result = (%t, %t)", gotDetached, gotMatched, wantDetached, wantMatched)
	}
	if gotDetached || gotMatched {
		t.Fatalf("missing tmux result = (%t, %t), want unknown (false, false)", gotDetached, gotMatched)
	}
}
