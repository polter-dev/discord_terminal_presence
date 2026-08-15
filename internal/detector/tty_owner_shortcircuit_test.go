package detector

import (
	"testing"
	"time"
)

// TestEnrichSkipsTTYWorkForUnownedProcess reproduces #566: before the fix,
// enrich() always ran TTY resolution, the tmux query, and the atime stat for
// every candidate, even though the selector's ownership gate
// (detector.go's `if !proc.Owned { continue }`) discards every result for a
// foreign process. On Unix that means stat-ing another user's TTY device per
// foreign candidate per scan; on Windows windowsTTYResolver.Resolve spawns a
// child termp.exe per foreign matched PID per scan. Ownership must still be
// resolved unconditionally (it is the security boundary), but everything
// downstream of it must be skipped once a process is known not to be owned.
func TestEnrichSkipsTTYWorkForUnownedProcess(t *testing.T) {
	var resolveCalls, tmuxCalls, atimeCalls int
	resolver := countingTTYResolver{
		fakeTTYResolver: fakeTTYResolver{resolutions: map[int32]TTYResolution{7: {Path: "/dev/pts/1"}}},
		calls:           &resolveCalls,
	}
	tmux := countingTmuxSnapshot{
		fakeTmuxSnapshot: fakeTmuxSnapshot{known: map[string]bool{"/dev/pts/1": true}},
		calls:            &tmuxCalls,
	}
	atime := countingAtimeSource{
		fakeAtimeSource: fakeAtimeSource{atimes: map[string]time.Time{"/dev/pts/1": time.Now()}},
		calls:           &atimeCalls,
	}
	owner := fakeOwnerResolver{owned: map[int32]bool{7: false}}

	enricher := newPresenceProcessEnricher(nil, resolver, tmux, atime, owner)
	result := enricher.(interface{ Enrich(Process) Process }).Enrich(Process{Pid: 7})

	if result.Owned {
		t.Fatalf("expected Owned=false for pid 7, got true")
	}
	if resolveCalls != 0 {
		t.Fatalf("TTYResolver.Resolve called %d times for an unowned process, want 0", resolveCalls)
	}
	if tmuxCalls != 0 {
		t.Fatalf("TmuxPaneSnapshot.Detached called %d times for an unowned process, want 0", tmuxCalls)
	}
	if atimeCalls != 0 {
		t.Fatalf("TTYAtimeSource.Atime called %d times for an unowned process, want 0", atimeCalls)
	}
}

// TestEnrichStillResolvesTTYForOwnedProcess guards against an over-eager
// fix: ownership must still be resolved unconditionally, and TTY/tmux/atime
// resolution must still run in full for an owned process.
func TestEnrichStillResolvesTTYForOwnedProcess(t *testing.T) {
	var resolveCalls, tmuxCalls, atimeCalls int
	resolver := countingTTYResolver{
		fakeTTYResolver: fakeTTYResolver{resolutions: map[int32]TTYResolution{7: {Path: "/dev/pts/1"}}},
		calls:           &resolveCalls,
	}
	tmux := countingTmuxSnapshot{
		fakeTmuxSnapshot: fakeTmuxSnapshot{known: map[string]bool{"/dev/pts/1": true}},
		calls:            &tmuxCalls,
	}
	atime := countingAtimeSource{
		fakeAtimeSource: fakeAtimeSource{atimes: map[string]time.Time{"/dev/pts/1": time.Now()}},
		calls:           &atimeCalls,
	}
	owner := fakeOwnerResolver{owned: map[int32]bool{7: true}}

	enricher := newPresenceProcessEnricher(nil, resolver, tmux, atime, owner)
	result := enricher.(interface{ Enrich(Process) Process }).Enrich(Process{Pid: 7})

	if !result.Owned {
		t.Fatalf("expected Owned=true for pid 7, got false")
	}
	if resolveCalls != 1 {
		t.Fatalf("TTYResolver.Resolve called %d times for an owned process, want 1", resolveCalls)
	}
	if atimeCalls != 1 {
		t.Fatalf("TTYAtimeSource.Atime called %d times for an owned process, want 1", atimeCalls)
	}
}

// countingTTYResolver wraps fakeTTYResolver and counts calls to Resolve.
type countingTTYResolver struct {
	fakeTTYResolver
	calls *int
}

func (c countingTTYResolver) Resolve(pid int32) (TTYResolution, error) {
	*c.calls++
	return c.fakeTTYResolver.Resolve(pid)
}

// countingTmuxSnapshot wraps fakeTmuxSnapshot and counts calls to Detached.
type countingTmuxSnapshot struct {
	fakeTmuxSnapshot
	calls *int
}

func (c countingTmuxSnapshot) Detached(tty string) (bool, bool) {
	*c.calls++
	return c.fakeTmuxSnapshot.Detached(tty)
}

// countingAtimeSource wraps fakeAtimeSource and counts calls to Atime.
type countingAtimeSource struct {
	fakeAtimeSource
	calls *int
}

func (c countingAtimeSource) Atime(tty string) (time.Time, error) {
	*c.calls++
	return c.fakeAtimeSource.Atime(tty)
}
