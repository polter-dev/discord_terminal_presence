package detector

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const episodeStateFile = "presence.json"

// Episode records one continuous process-presence interval.
type Episode struct {
	PresentSince time.Time `json:"present_since"`
	TTY          string    `json:"tty,omitempty"`
	LastAtime    time.Time `json:"last_atime,omitempty"`
}

// EpisodeStore holds active presence episodes keyed by tool, PID, and create time.
type EpisodeStore struct {
	mu       sync.RWMutex
	Episodes map[string]Episode `json:"episodes"`
	observed map[string]bool
}

type diskEpisodes struct {
	Episodes map[string]Episode `json:"episodes"`
}

func EpisodeStatePath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "termp", episodeStateFile)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join("termp", episodeStateFile)
	}
	return filepath.Join(home, ".local", "state", "termp", episodeStateFile)
}

func EpisodeKey(toolID string, pid int32, createTime time.Time) string {
	return fmt.Sprintf("%s\x00%d\x00%d", toolID, pid, createTime.UnixNano())
}

func NewEpisodeStore() *EpisodeStore {
	return &EpisodeStore{Episodes: make(map[string]Episode), observed: make(map[string]bool)}
}

// Observe returns the episode anchor for an eligible process. A loaded episode
// is preserved only when its tty identity and atime history make continuity clear.
func (s *EpisodeStore) Observe(key string, tty TTYInfo, now time.Time, timeout time.Duration) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Episodes == nil {
		s.Episodes = make(map[string]Episode)
	}
	if s.observed == nil {
		s.observed = make(map[string]bool)
	}
	if episode, ok := s.Episodes[key]; ok && s.observed[key] {
		if tty.State == TTYResolved && tty.AtimeKnown {
			episode.TTY = tty.Path
			episode.LastAtime = tty.Atime
			s.Episodes[key] = episode
		}
		return episode.PresentSince, false
	}

	anchor := now
	if loaded, ok := s.Episodes[key]; ok && canResumeEpisode(loaded, tty, timeout) {
		anchor = loaded.PresentSince
	}
	episode := Episode{PresentSince: anchor}
	if tty.State == TTYResolved && tty.AtimeKnown {
		episode.TTY = tty.Path
		episode.LastAtime = tty.Atime
	}
	s.Episodes[key] = episode
	s.observed[key] = true
	return anchor, true
}

func canResumeEpisode(episode Episode, tty TTYInfo, timeout time.Duration) bool {
	if episode.PresentSince.IsZero() || timeout <= 0 || tty.State != TTYResolved || !tty.AtimeKnown ||
		episode.TTY == "" || episode.TTY != tty.Path || episode.LastAtime.IsZero() {
		return false
	}
	delta := tty.Atime.Sub(episode.LastAtime)
	return delta >= 0 && delta <= timeout
}

func (s *EpisodeStore) EndAbsent(eligible map[string]struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for key := range s.Episodes {
		if _, ok := eligible[key]; !ok {
			delete(s.Episodes, key)
			delete(s.observed, key)
			changed = true
		}
	}
	return changed
}

func LoadEpisodeStore(path string) (*EpisodeStore, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewEpisodeStore(), nil
	}
	if err != nil {
		return NewEpisodeStore(), err
	}
	var disk diskEpisodes
	if err := json.Unmarshal(data, &disk); err != nil {
		return NewEpisodeStore(), nil
	}
	store := NewEpisodeStore()
	for key, episode := range disk.Episodes {
		store.Episodes[key] = episode
	}
	return store, nil
}

func SaveEpisodeStore(path string, store *EpisodeStore) error {
	if store == nil {
		store = NewEpisodeStore()
	}
	store.mu.RLock()
	snapshot := diskEpisodes{Episodes: make(map[string]Episode, len(store.Episodes))}
	for key, episode := range store.Episodes {
		snapshot.Episodes[key] = episode
	}
	store.mu.RUnlock()

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
