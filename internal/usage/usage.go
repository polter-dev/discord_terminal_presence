package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

const (
	defaultStateDir  = ".local/state"
	appStateDir      = "termp"
	defaultStateFile = "usage.json"
)

// Entry records local-only usage for one known tool.
type Entry struct {
	Count    int       `json:"count"`
	LastSeen time.Time `json:"last_seen"`
	Seconds  int64     `json:"seconds,omitempty"`
}

// Store is a concurrency-safe usage map keyed by tool ID.
type Store struct {
	mu    sync.RWMutex
	Tools map[string]Entry `json:"tools"`
}

type diskStore struct {
	Tools map[string]Entry `json:"tools"`
}

type statePathResolver struct {
	goos         string
	getenv       func(string) string
	userHomeDir  func() (string, error)
	userCacheDir func() (string, error)
	stat         func(string) (os.FileInfo, error)
	copyFile     func(string, string) error
}

// StatePath returns the XDG-aware usage state path.
func StatePath() string {
	return statePathFor(statePathResolver{
		goos:         runtime.GOOS,
		getenv:       os.Getenv,
		userHomeDir:  os.UserHomeDir,
		userCacheDir: os.UserCacheDir,
		stat:         os.Stat,
		copyFile:     copyStateFileBestEffort,
	})
}

func statePathFor(resolver statePathResolver) string {
	if resolver.goos == "windows" {
		return windowsStatePathFor(resolver)
	}
	if xdg := resolver.getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, appStateDir, defaultStateFile)
	}
	home, err := resolver.userHomeDir()
	if err != nil || home == "" {
		return filepath.Join(appStateDir, defaultStateFile)
	}
	return filepath.Join(home, defaultStateDir, appStateDir, defaultStateFile)
}

func windowsStatePathFor(resolver statePathResolver) string {
	native := filepath.Join(appStateDir, defaultStateFile)
	if cacheDir, err := resolver.userCacheDir(); err == nil && cacheDir != "" {
		native = filepath.Join(cacheDir, appStateDir, defaultStateFile)
	}
	home, err := resolver.userHomeDir()
	if err != nil || home == "" {
		return native
	}
	legacy := filepath.Join(home, defaultStateDir, appStateDir, defaultStateFile)
	return migrateStatePath(native, legacy, resolver)
}

func migrateStatePath(native, legacy string, resolver statePathResolver) string {
	// Prefer the native Windows path, but copy an existing legacy file forward
	// before using it; if migration fails, keep reading the legacy file.
	if _, err := resolver.stat(native); err == nil {
		return native
	}
	if _, err := resolver.stat(legacy); err != nil {
		return native
	}
	if err := resolver.copyFile(legacy, native); err != nil {
		return legacy
	}
	return native
}

func copyStateFileBestEffort(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	dir := filepath.Dir(to)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(to)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, to)
}

// New returns an empty usage store.
func New() *Store {
	return &Store{Tools: make(map[string]Entry)}
}

// Load reads a usage store from path. Missing or corrupt files return an empty store.
func Load(path string) (*Store, error) {
	data, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return New(), err
	}
	var disk diskStore
	if err := json.Unmarshal(data, &disk); err != nil {
		return New(), nil
	}
	store := New()
	for id, entry := range disk.Tools {
		store.Tools[id] = entry
	}
	return store, nil
}

// Save writes store to path atomically.
func Save(path string, store *Store) error {
	if store == nil {
		store = New()
	}
	snapshot := store.snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmpPath, path)
}

// Record marks toolID as seen at now.
func (s *Store) Record(toolID string, now time.Time) {
	if toolID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Tools == nil {
		s.Tools = make(map[string]Entry)
	}
	entry := s.Tools[toolID]
	entry.Count++
	entry.LastSeen = now
	s.Tools[toolID] = entry
}

// Rank returns tool IDs ordered by usage count, then recency.
func (s *Store) Rank() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.Tools))
	for id := range s.Tools {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		left := s.Tools[ids[i]]
		right := s.Tools[ids[j]]
		if left.Count != right.Count {
			return left.Count > right.Count
		}
		if !left.LastSeen.Equal(right.LastSeen) {
			return left.LastSeen.After(right.LastSeen)
		}
		return ids[i] < ids[j]
	})
	return ids
}

func (s *Store) snapshot() diskStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := diskStore{Tools: make(map[string]Entry, len(s.Tools))}
	for id, entry := range s.Tools {
		out.Tools[id] = entry
	}
	return out
}
