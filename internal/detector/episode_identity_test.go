package detector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// legacyZeroCreateTimeKey is the episode key a pre-#623 build wrote for a
// process whose creation time could not be read: EpisodeKey formatted the
// zero time.Time through UnixNano, which overflows to one constant for every
// such process.
func legacyZeroCreateTimeKey(toolID string, pid int32) string {
	return fmt.Sprintf("%s\x00%d\x00%d", toolID, pid, time.Time{}.UnixNano())
}

// TestEpisodeResumeRequiresKnownProcessIdentity is the #623 regression, with
// the #617 behavior it must not disturb as its control. Both subtests are the
// same scenario — a daemon restart on a stock Linux relatime/noatime mount,
// where TTYInfo.AtimeKnown is never true — and differ only in whether the
// process creation time could be read.
func TestEpisodeResumeRequiresKnownProcessIdentity(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	restart := base.Add(time.Hour)
	// A relatime/noatime mount: resolved TTY, no atime. This is exactly the
	// configuration #617 fixed, so it is the configuration this fix has to
	// leave working.
	relatimeTTY := TTYInfo{State: TTYResolved, Path: "/dev/pts/3", AtimeKnown: false}

	tests := []struct {
		name       string
		createTime time.Time
		wantResume bool
	}{
		{
			// #617 control: a known creation time identifies this exact OS
			// process instance, so the anchor must survive the restart even
			// though no atime is knowable.
			name:       "known create time resumes",
			createTime: base.Add(-time.Hour),
			wantResume: true,
		},
		{
			// #623: an unreadable creation time leaves CreateTime zero
			// (processIdentity, gopsutil.go), so the key does not identify a
			// process instance — every process sharing this tool and pid
			// collides on it. Resuming would inherit an unrelated earlier
			// session's start time.
			name:       "unknown create time starts a fresh anchor",
			createTime: time.Time{},
			wantResume: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "presence.json")
			store := NewEpisodeStore()
			key := EpisodeKey("claude-code", 7, tc.createTime)
			anchor, _ := store.Observe(key, relatimeTTY, base)
			if !anchor.Equal(base) {
				t.Fatalf("first anchor = %s, want %s", anchor, base)
			}
			if err := SaveEpisodeStore(path, store); err != nil {
				t.Fatal(err)
			}

			loaded, err := LoadEpisodeStore(path)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := loaded.Observe(EpisodeKey("claude-code", 7, tc.createTime), relatimeTTY, restart)

			want := restart
			if tc.wantResume {
				want = base
			}
			if !got.Equal(want) {
				t.Fatalf("anchor after restart = %s, want %s (resume=%t)", got, want, tc.wantResume)
			}
		})
	}
}

// TestEpisodeLegacyZeroCreateTimeKeyDoesNotResume covers an episode persisted
// by a pre-#623 build, whose key encodes the zero creation time as the
// overflowed UnixNano constant. That entry must not resume anything either:
// no key this build constructs can equal it, so it is unreachable rather than
// gated, and EndAbsent sweeps it out.
func TestEpisodeLegacyZeroCreateTimeKeyDoesNotResume(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "presence.json")
	legacy := legacyZeroCreateTimeKey("claude-code", 7)
	disk, err := json.Marshal(map[string]any{
		"episodes": map[string]any{legacy: map[string]any{"present_since": base}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, disk, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadEpisodeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Episodes[legacy].PresentSince; !got.Equal(base) {
		t.Fatalf("fixture did not load: present_since = %s, want %s", got, base)
	}

	restart := base.Add(time.Hour)
	tty := TTYInfo{State: TTYResolved, Path: "/dev/pts/3"}
	got, _ := loaded.Observe(EpisodeKey("claude-code", 7, time.Time{}), tty, restart)
	if !got.Equal(restart) {
		t.Fatalf("anchor from a legacy zero-create-time entry = %s, want a fresh %s", got, restart)
	}
}

// TestEpisodeUnknownCreateTimeAnchorIsStableWithinRun guards the failure mode
// this fix could have introduced: declining to resume must apply only to an
// episode loaded from disk, never to one this daemon already observed, or the
// elapsed timer would restart on every scan instead of only across restarts.
func TestEpisodeUnknownCreateTimeAnchorIsStableWithinRun(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := NewEpisodeStore()
	key := EpisodeKey("claude-code", 7, time.Time{})
	tty := TTYInfo{State: TTYResolved, Path: "/dev/pts/3"}

	first, _ := store.Observe(key, tty, base)
	for i := 1; i <= 3; i++ {
		got, _ := store.Observe(key, tty, base.Add(time.Duration(i)*30*time.Second))
		if !got.Equal(first) {
			t.Fatalf("scan %d anchor = %s, want the unchanged %s", i, got, first)
		}
	}
}

// TestEpisodeKeyIdentityKnown pins the invariant the resume gate depends on:
// EpisodeKey and episodeIdentityKnown must agree about which keys carry a
// real process creation time, including for keys written by older builds.
func TestEpisodeKeyIdentityKnown(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !episodeIdentityKnown(EpisodeKey("claude-code", 7, created)) {
		t.Error("a key built from a real create time must report a known identity")
	}
	if episodeIdentityKnown(EpisodeKey("claude-code", 7, time.Time{})) {
		t.Error("a key built from the zero create time must report an unknown identity")
	}
	if EpisodeKey("claude-code", 7, time.Time{}) == legacyZeroCreateTimeKey("claude-code", 7) {
		t.Error("the unknown-identity key must be distinguishable from a formatted timestamp")
	}
}
