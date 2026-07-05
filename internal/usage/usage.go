package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// StatePath returns the XDG-aware usage state path.
func StatePath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, appStateDir, defaultStateFile)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(appStateDir, defaultStateFile)
	}
	return filepath.Join(home, defaultStateDir, appStateDir, defaultStateFile)
}

// New returns an empty usage store.
func New() *Store {
	return &Store{Tools: make(map[string]Entry)}
}

// Load reads a usage store from path. Missing or corrupt files return an empty store.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
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
	return os.Rename(tmpPath, path)
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
