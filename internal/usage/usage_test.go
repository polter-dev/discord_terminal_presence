package usage

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestStatePathForOSBranches(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	stateHome := filepath.Join(root, "xdg-state")
	localAppData := filepath.Join(root, "AppData", "Local")
	resolver := func(goos string) statePathResolver {
		return statePathResolver{
			goos: goos,
			getenv: func(key string) string {
				if key == "XDG_STATE_HOME" {
					return stateHome
				}
				return ""
			},
			userHomeDir:  func() (string, error) { return home, nil },
			userCacheDir: func() (string, error) { return localAppData, nil },
			stat:         os.Stat,
			copyFile:     copyStateFileBestEffort,
		}
	}

	tests := []struct {
		name string
		goos string
		want string
	}{
		{
			name: "windows uses local app data",
			goos: "windows",
			want: filepath.Join(localAppData, appStateDir, defaultStateFile),
		},
		{
			name: "linux honors xdg",
			goos: "linux",
			want: filepath.Join(stateHome, appStateDir, defaultStateFile),
		},
		{
			name: "darwin honors xdg",
			goos: "darwin",
			want: filepath.Join(stateHome, appStateDir, defaultStateFile),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statePathFor(resolver(tt.goos)); got != tt.want {
				t.Fatalf("statePathFor(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}

func TestStatePathForUnixHomeFallbackUnchanged(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	resolver := statePathResolver{
		goos:         "linux",
		getenv:       func(string) string { return "" },
		userHomeDir:  func() (string, error) { return home, nil },
		userCacheDir: func() (string, error) { return t.TempDir(), nil },
		stat:         os.Stat,
		copyFile:     copyStateFileBestEffort,
	}

	got := statePathFor(resolver)
	want := filepath.Join(home, defaultStateDir, appStateDir, defaultStateFile)
	if got != want {
		t.Fatalf("statePathFor(linux) = %q, want %q", got, want)
	}
}

func TestStatePathWindowsMigration(t *testing.T) {
	makeResolver := func(root string, copyFile func(string, string) error) (statePathResolver, string, string) {
		home := filepath.Join(root, "home")
		localAppData := filepath.Join(root, "AppData", "Local")
		native := filepath.Join(localAppData, appStateDir, defaultStateFile)
		legacy := filepath.Join(home, defaultStateDir, appStateDir, defaultStateFile)
		return statePathResolver{
			goos:         "windows",
			getenv:       func(string) string { return "" },
			userHomeDir:  func() (string, error) { return home, nil },
			userCacheDir: func() (string, error) { return localAppData, nil },
			stat:         os.Stat,
			copyFile:     copyFile,
		}, native, legacy
	}
	writeUsage := func(t *testing.T, path, toolID string) {
		t.Helper()
		store := New()
		store.Record(toolID, time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC))
		if err := Save(path, store); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("new path present ignores legacy", func(t *testing.T) {
		resolver, native, legacy := makeResolver(t.TempDir(), copyStateFileBestEffort)
		writeUsage(t, native, "native")
		writeUsage(t, legacy, "legacy")

		path := statePathFor(resolver)
		store, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != native {
			t.Fatalf("path = %q, want %q", path, native)
		}
		if _, ok := store.Tools["native"]; !ok {
			t.Fatalf("native usage missing: %#v", store.Tools)
		}
		if _, ok := store.Tools["legacy"]; ok {
			t.Fatalf("legacy usage was read despite native file: %#v", store.Tools)
		}
	})

	t.Run("legacy only is read and copied", func(t *testing.T) {
		resolver, native, legacy := makeResolver(t.TempDir(), copyStateFileBestEffort)
		writeUsage(t, legacy, "legacy")

		path := statePathFor(resolver)
		store, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != native {
			t.Fatalf("path = %q, want %q", path, native)
		}
		if _, ok := store.Tools["legacy"]; !ok {
			t.Fatalf("legacy usage missing after migration: %#v", store.Tools)
		}
		if _, err := os.Stat(native); err != nil {
			t.Fatalf("migrated usage state missing: %v", err)
		}
		nativeData, err := os.ReadFile(native)
		if err != nil {
			t.Fatal(err)
		}
		legacyData, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if string(nativeData) != string(legacyData) {
			t.Fatalf("migrated usage state = %q, want full legacy contents %q", nativeData, legacyData)
		}
	})

	t.Run("neither present uses native empty store", func(t *testing.T) {
		resolver, native, _ := makeResolver(t.TempDir(), copyStateFileBestEffort)

		path := statePathFor(resolver)
		store, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != native || len(store.Tools) != 0 {
			t.Fatalf("path = %q tools = %#v, want native empty store", path, store.Tools)
		}
	})

	t.Run("copy failure still reads legacy", func(t *testing.T) {
		resolver, native, legacy := makeResolver(t.TempDir(), func(string, string) error {
			return errors.New("copy failed")
		})
		writeUsage(t, legacy, "legacy")

		path := statePathFor(resolver)
		store, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != legacy {
			t.Fatalf("path = %q, want %q", path, legacy)
		}
		if _, ok := store.Tools["legacy"]; !ok {
			t.Fatalf("legacy usage missing after failed migration: %#v", store.Tools)
		}
		if _, err := os.Stat(native); !os.IsNotExist(err) {
			t.Fatalf("native usage state err = %v, want not exist", err)
		}
	})
}

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
	t.Setenv("LOCALAPPDATA", root)
	got := StatePath()
	want := filepath.Join(root, appStateDir, defaultStateFile)
	if got != want {
		t.Fatalf("StatePath() = %q, want %q", got, want)
	}
}

func TestConcurrentRecordRankSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "usage.json")
	store := New()
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	toolIDs := []string{"codex-cli", "claude-code", "gemini-cli", "lazygit"}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				store.Record(toolIDs[(worker+i)%len(toolIDs)], base.Add(time.Duration(worker*100+i)*time.Second))
				_ = store.Rank()
				if i%10 == 0 {
					if err := Save(path, store); err != nil {
						errs <- err
						return
					}
					if _, err := Load(path); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if err := Save(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Rank()) != len(toolIDs) {
		t.Fatalf("loaded rank = %#v, want %d tools", loaded.Rank(), len(toolIDs))
	}
}
