package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Manager owns the current last-good config and watches it for edits.
type Manager struct {
	path    string
	mu      sync.RWMutex
	current Config
	lastErr error
	changes chan Config
}

// NewManager loads the config at the default path. If the file is malformed,
// defaults remain current and LastError exposes the parse/validation error.
func NewManager() *Manager {
	return NewManagerPath(DefaultPath())
}

// NewManagerPath loads the config at path.
func NewManagerPath(path string) *Manager {
	cfg, err := LoadPath(path)
	if err != nil {
		cfg = DefaultWithPath(path)
	}
	return &Manager{
		path:    path,
		current: cfg,
		lastErr: err,
		changes: make(chan Config, 1),
	}
}

// Current returns a copy of the current last-good config and latest load error.
func (m *Manager) Current() (Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneConfig(m.current), m.lastErr
}

// LastError returns the latest load/reload error.
func (m *Manager) LastError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastErr
}

// Changes emits a config copy after each successful reload.
func (m *Manager) Changes() <-chan Config {
	return m.changes
}

// Reload reloads the file, preserving the last-good config on error.
func (m *Manager) Reload() error {
	cfg, err := LoadPath(m.path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lastErr = err
		return err
	}
	m.current = cfg
	m.lastErr = nil
	select {
	case m.changes <- cloneConfig(cfg):
	default:
	}
	return nil
}

// Watch reloads config changes until ctx is cancelled.
func (m *Manager) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Clean(event.Name) != filepath.Clean(m.path) {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove) {
					_ = m.Reload()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				m.mu.Lock()
				m.lastErr = err
				m.mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

// EnsureConfigDir creates the config directory for callers that want to watch before the file exists.
func EnsureConfigDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
