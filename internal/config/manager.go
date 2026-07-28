package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Manager owns the current last-good config and watches it for edits.
type Manager struct {
	path             string
	reloadMu         sync.Mutex
	mu               sync.RWMutex
	current          Config
	accepted         fileSnapshot
	lastErr          error
	pendingLoosening pendingEnabledLoosening
	reloads          chan ReloadResult
	watchErrs        chan error
}

type pendingEnabledLoosening struct {
	snapshot  fileSnapshot
	firstSeen time.Time
	retry     *time.Timer
}

// enabledLooseningHorizon is a judgment call: three seconds covers realistic
// multi-step editor writes without making an intentional reset unreasonably
// slow. A valid partial that omits enabled and remains stalled beyond this
// horizon is indistinguishable from a deliberate blank and can still
// re-enable presence.
const enabledLooseningHorizon = 3 * time.Second

// ReloadResult reports the newest automatic or explicit reload outcome.
type ReloadResult struct {
	Config Config
	Err    error
}

// NewManager loads the config at the default path. If an existing file cannot
// be loaded, presence remains disabled and LastError exposes the load error.
func NewManager() *Manager {
	return NewManagerPath(DefaultPath())
}

// NewManagerPath loads the config at path.
func NewManagerPath(path string) *Manager {
	snap := snapshotConfigFile(path)
	cfg, err := loadSnapshot(path, snap)
	accepted := fileSnapshot{}
	if err == nil {
		accepted = snap
	}
	return &Manager{
		path:      path,
		current:   cfg,
		accepted:  accepted,
		lastErr:   err,
		reloads:   make(chan ReloadResult, 1),
		watchErrs: make(chan error, 1),
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

// Reloads emits the latest reload result. Bursty results may be coalesced when
// the consumer has not caught up, preserving the newest success or failure.
func (m *Manager) Reloads() <-chan ReloadResult {
	return m.reloads
}

// WatchErrors emits watcher-backend failures without changing config validity.
func (m *Manager) WatchErrors() <-chan error {
	return m.watchErrs
}

// Reload reloads the file, preserving the last-good config on error.
//
// Before accepting a result, it waits for the file to settle (consecutive
// reads agreeing). A false-to-true enabled transition caused by an absent key
// then has to remain byte-identical across enabledLooseningHorizon; explicit
// enabled=true and every other transition keep the normal settle latency.
// See settledConfigSnapshot and #410. If the file is still changing when the
// settle budget runs out, Reload is a no-op: it leaves last-good and
// LastError untouched and returns nil, relying on a later fsnotify event
// (fired when the write finishes) to trigger another attempt.
func (m *Manager) Reload() error {
	return m.reload(true)
}

func (m *Manager) reload(scheduleRetry bool) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	m.mu.RLock()
	accepted := m.accepted
	m.mu.RUnlock()
	snap, ok := settledConfigSnapshot(m.path, accepted)
	if !ok {
		return nil
	}
	cfg, enabledDefined, err := loadSnapshotWithMetadata(m.path, snap)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.clearPendingLooseningLocked()
		m.lastErr = err
		m.publishReloadLocked(ReloadResult{Err: err})
		return err
	}
	m.acceptReloadLocked(cfg, snap, enabledDefined, scheduleRetry)
	return nil
}

// acceptReloadLocked is the only place a successfully decoded reload can
// replace current. Keeping the enabled guard at this state-commit choke point
// makes it impossible for a new reload path to bypass the rule accidentally.
func (m *Manager) acceptReloadLocked(cfg Config, snap fileSnapshot, enabledDefined, scheduleRetry bool) {
	if !m.current.Enabled && cfg.Enabled && !enabledDefined {
		now := time.Now()
		if !snapshotsEqual(m.pendingLoosening.snapshot, snap) || m.pendingLoosening.firstSeen.IsZero() {
			m.clearPendingLooseningLocked()
			m.pendingLoosening.snapshot = snap
			m.pendingLoosening.firstSeen = now
		}
		elapsed := now.Sub(m.pendingLoosening.firstSeen)
		if elapsed < enabledLooseningHorizon {
			if scheduleRetry && m.pendingLoosening.retry == nil {
				delay := enabledLooseningHorizon - elapsed
				m.pendingLoosening.retry = time.AfterFunc(delay, func() {
					_ = m.reload(false)
				})
			}
			return
		}
	}

	m.clearPendingLooseningLocked()
	m.current = cfg
	m.accepted = snap
	m.lastErr = nil
	m.publishReloadLocked(ReloadResult{Config: cloneConfig(cfg)})
}

func (m *Manager) clearPendingLooseningLocked() {
	if m.pendingLoosening.retry != nil {
		m.pendingLoosening.retry.Stop()
	}
	m.pendingLoosening = pendingEnabledLoosening{}
}

func (m *Manager) publishReloadLocked(next ReloadResult) {
	select {
	case m.reloads <- next:
	default:
		select {
		case <-m.reloads:
		default:
		}
		select {
		case m.reloads <- next:
		default:
		}
	}
}

func (m *Manager) reportWatchFailure(err error) {
	select {
	case m.watchErrs <- err:
	default:
		select {
		case <-m.watchErrs:
		default:
		}
		select {
		case m.watchErrs <- err:
		default:
		}
	}
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
				m.reportWatchFailure(err)
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
