package usage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecordRankOrdersByFrequencyThenRecency(t *testing.T) {
	store := New()
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	store.Record("codex-cli", base)
	store.Record("claude-code", base.Add(time.Minute))
	store.Record("codex-cli", base.Add(2*time.Minute))
	store.Record("gemini-cli", base.Add(3*time.Minute))

	got := store.Rank()
	want := []string{"codex-cli", "gemini-cli", "claude-code"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Rank() = %#v, want %#v", got, want)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "usage.json")
	store := New()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	store.Record("codex-cli", now)
	store.Record("codex-cli", now.Add(time.Second))

	if err := Save(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := loaded.Tools["codex-cli"]
	if entry.Count != 2 || !entry.LastSeen.Equal(now.Add(time.Second)) {
		t.Fatalf("loaded entry = %#v", entry)
	}
}

func TestLoadMissingAndCorruptAreEmpty(t *testing.T) {
	dir := t.TempDir()
	missing, err := Load(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Rank()) != 0 {
		t.Fatalf("missing Rank() = %#v, want empty", missing.Rank())
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte(`{not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Rank()) != 0 {
		t.Fatalf("corrupt Rank() = %#v, want empty", loaded.Rank())
	}
}

func TestStatePathHonorsXDGStateHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	got := StatePath()
	want := filepath.Join(root, appStateDir, defaultStateFile)
	if got != want {
		t.Fatalf("StatePath() = %q, want %q", got, want)
	}
}
