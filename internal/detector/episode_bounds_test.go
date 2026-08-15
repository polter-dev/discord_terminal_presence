package detector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadEpisodeStoreRejectsOversizedFile reproduces the second half of
// #567: LoadEpisodeStore had no size bound at all, unlike its sibling
// internal/usage.Load. A 55 MB presence.json loaded fine and cost 202 MiB of
// heap. This asserts the same bound usage.go already enforces.
func TestLoadEpisodeStoreRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presence.json")
	if err := os.WriteFile(path, make([]byte, maxEpisodeStateFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := LoadEpisodeStore(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadEpisodeStore() = (%#v, %v), want oversized file error", store, err)
	}
}

// TestLoadEpisodeStoreEnforcesEntryCap reproduces the entry-cap half of
// #567: an unbounded episode count on disk had no cap, unlike
// internal/usage's maxEntries. This writes more than maxEpisodeEntries
// episodes directly (bypassing Observe, which only ever grows with real
// running processes) and checks the load caps it down, keeping the most
// recently active entries.
func TestLoadEpisodeStoreEnforcesEntryCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presence.json")
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	disk := diskEpisodes{Episodes: make(map[string]Episode, maxEpisodeEntries+50)}
	for i := 0; i < maxEpisodeEntries+50; i++ {
		key := EpisodeKey("claude-code", int32(i), base)
		disk.Episodes[key] = Episode{
			PresentSince: base,
			LastAtime:    base.Add(time.Duration(i) * time.Second),
		}
	}
	newestKey := EpisodeKey("claude-code", int32(maxEpisodeEntries+49), base)
	oldestKey := EpisodeKey("claude-code", 0, base)

	writeDiskEpisodes(t, path, disk)

	store, err := LoadEpisodeStore(path)
	if err != nil {
		t.Fatalf("LoadEpisodeStore() = %v, want a bounded load to succeed", err)
	}
	if len(store.Episodes) != maxEpisodeEntries {
		t.Fatalf("loaded %d episodes, want capped at %d", len(store.Episodes), maxEpisodeEntries)
	}
	if _, ok := store.Episodes[newestKey]; !ok {
		t.Fatal("cap enforcement dropped the most recently active episode")
	}
	if _, ok := store.Episodes[oldestKey]; ok {
		t.Fatal("cap enforcement kept the least recently active episode")
	}
}

func writeDiskEpisodes(t *testing.T, path string, disk diskEpisodes) {
	t.Helper()
	data, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunDoesNotPersistOverCorruptEpisodeFile reproduces the caller-side half
// of #567: before the fix, a corrupt presence.json was treated identically
// to an absent one, so the detector's run loop happily saved its fresh
// in-memory (empty) episode store back over the file on exit, destroying the
// user's real history. With the fix, a load error disables persistence for
// that run entirely.
func TestRunDoesNotPersistOverCorruptEpisodeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presence.json")
	corrupt := []byte("not json at all")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	lister := newControlledLister()
	det, err := New(testRegistry(t), lister, Config{ScanInterval: time.Nanosecond, DebounceCycles: 1})
	if err != nil {
		t.Fatal(err)
	}
	det.presenceStatePath = path

	ctx, cancel := context.WithCancel(context.Background())
	ch := det.Run(ctx)

	select {
	case <-lister.calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for process scan")
	}
	lister.results <- processListResult{processes: nil}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for detection")
	}

	cancel()
	// Keep answering any in-flight or racing scan so run() can observe
	// ctx.Done() and return, until the output channel closes (confirming
	// run() returned and its deferred save, if any, already ran).
drain:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break drain
			}
		case <-lister.calls:
			select {
			case lister.results <- processListResult{processes: nil}:
			case <-time.After(time.Second):
				t.Fatal("timed out feeding a racing scan during shutdown")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for detector shutdown")
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("presence.json was rewritten after a corrupt load: got %q, want unchanged %q", got, corrupt)
	}
}
