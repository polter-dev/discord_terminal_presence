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
	horizon          time.Duration
	closed           bool
	// retries tracks armed loosening retries that have not finished. Close
	// waits on it so a retry that had already started when Close ran cannot
	// still be touching manager state after Close returns.
	retries   sync.WaitGroup
	reloads   chan ReloadResult
	watchErrs chan error
}

type pendingEnabledLoosening struct {
	snapshot  fileSnapshot
	firstSeen time.Time
	retry     *time.Timer
}

// enabledLooseningHorizon is shared by manager commits and whole-document
// update loads. Three seconds covers realistic multi-step editor writes
// without making an intentional reset unreasonably slow. A valid partial that
// omits enabled and remains stalled beyond this horizon is indistinguishable
// from a deliberate blank and can still re-enable presence.
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
	return newManagerPathWith(path, snapshotConfigFile, time.Now, time.Sleep)
}

func newManagerPathWith(
	path string,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
) *Manager {
	return newManagerPathWithHorizon(path, snapshot, now, sleep, enabledLooseningHorizon)
}

// newManagerPathWithHorizon is the construction seam. horizon is a parameter
// only so lifecycle tests can arm and observe the loosening retry in
// milliseconds instead of racing a three-second wall-clock wait; every
// production path passes enabledLooseningHorizon.
func newManagerPathWithHorizon(
	path string,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
	horizon time.Duration,
) *Manager {
	snap, settled := boundedSettledConfigSnapshotWith(path, snapshot, now, sleep, fileSnapshot{})
	cfg, enabledDefined, err := loadSnapshotWithMetadata(path, snap)
	accepted := fileSnapshot{}
	if err == nil {
		accepted = snap
	}
	// Construction has no previously accepted snapshot to compare a candidate
	// against, so the prefix rule in provisionalConfigSnapshot is inert here
	// and cannot recognise a non-atomic writer. The only bytes that can
	// certify presence-on at construction are bytes that settled AND said
	// enabled = true explicitly. Everything else reads exactly like a
	// genuinely defaulted config while it may in fact be a write in flight
	// (#548):
	//
	//   - an existing blank file (#440),
	//   - a writer's first chunk that happens to parse but omits enabled,
	//   - a snapshot that never settled inside the standalone bound, whose
	//     bytes are admittedly uncertified even when they do define enabled,
	//   - a missing file, which is a genuine first run or an unlink/recreate
	//     window (#448 route A).
	//
	// Seed presence off in all of those and route the candidate through the
	// same state-commit choke point every reload uses, with enabledDefined
	// forced false because construction cannot vouch for the bytes. That arms
	// the false-to-true loosening guard for the manager's whole life rather
	// than only on the blank branch, so a completed explicit opt-out applies
	// immediately while a config that really is a plain default still becomes
	// enabled one loosening horizon later.
	//
	// The three ambiguous shapes are named separately so each can be reasoned
	// about, and reverted, on its own.
	missingFile := err == nil && !snap.exists
	defaultedEnabled := err == nil && snap.exists && !enabledDefined
	unsettledSnapshot := err == nil && !settled
	uncertifiedEnabled := cfg.Enabled &&
		(missingFile || defaultedEnabled || unsettledSnapshot)
	pendingCfg := cfg
	if uncertifiedEnabled {
		cfg = invalidFallbackWithPath(path)
	}
	manager := &Manager{
		path:      path,
		current:   cfg,
		accepted:  accepted,
		lastErr:   err,
		horizon:   horizon,
		reloads:   make(chan ReloadResult, 1),
		watchErrs: make(chan error, 1),
	}
	if uncertifiedEnabled {
		// Arms the existing horizon without publishing a misleading successful
		// reload while presence is held off.
		//
		// The lock is not ceremony. acceptReloadLocked stores the timer it just
		// armed, and that timer's func reads the same field through reload, so
		// the constructor is already sharing the manager with another goroutine
		// by the time it writes. The horizon makes the window enormous in
		// practice, but the write is unsynchronized without this and the race
		// detector reports it as soon as a caller shortens the horizon.
		manager.mu.Lock()
		manager.acceptReloadLocked(pendingCfg, snap, false)
		manager.mu.Unlock()
	}
	return manager
}

// Close stops the manager's pending loosening retry and makes every later
// reload a no-op. It is safe to call more than once and from any goroutine.
//
// Close deliberately does NOT resolve a loosening decision that is still
// inside its horizon. A manager torn down mid-horizon simply stops: it does
// not commit the pending candidate, does not revert anything already
// committed, and does not publish a reload result. Presence therefore can
// never turn on as a side effect of shutdown (#553).
//
// Ordering matters for the race that time.Timer.Stop cannot win on its own. A
// retry that has already fired is not stopped by Stop returning false, so
// Close first marks the manager closed under the same lock every commit takes.
// A retry that is mid-flight then finds the manager closed and returns without
// touching state, and Close waits for it to finish before returning.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.clearPendingLooseningLocked()
	m.mu.Unlock()
	// Waiting outside the lock: a retry still running needs m.mu to observe
	// the closed flag it is about to give up on.
	m.retries.Wait()
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
//
// After Close, Reload is also a no-op and returns nil.
func (m *Manager) Reload() error {
	return m.reload()
}

func (m *Manager) reload() error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	m.mu.RLock()
	closed := m.closed
	accepted := m.accepted
	m.mu.RUnlock()
	if closed {
		return nil
	}
	snap, ok := settledConfigSnapshot(m.path, accepted)
	if !ok {
		return nil
	}
	cfg, enabledDefined, err := loadSnapshotWithMetadata(m.path, snap)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-checked under the write lock: Close may have run while this reload
	// was reading the file. Committing or publishing now would let shutdown
	// resolve a decision it is only allowed to stop.
	if m.closed {
		return nil
	}
	if err != nil {
		m.clearPendingLooseningLocked()
		m.lastErr = err
		m.publishReloadLocked(ReloadResult{Err: err})
		return err
	}
	m.acceptReloadLocked(cfg, snap, enabledDefined)
	return nil
}

// acceptReloadLocked is the only place a successfully decoded reload can
// replace current. Keeping the loosening guard at this state-commit choke
// point makes it impossible for a new reload path to bypass the rule
// accidentally, and applying it to ANY privacy-loosening transition (not just
// the global enabled flag) is what #447 requires: a truncating writer that
// drops or widens a directory allowlist, drops a show_directory/basename
// override, or drops a per-tool opt-out is exactly as dangerous as one that
// drops enabled, and used to have no protection at all.
func (m *Manager) acceptReloadLocked(cfg Config, snap fileSnapshot, enabledDefined bool) {
	ambiguousEnabledLoosening := !m.current.Enabled && cfg.Enabled && !enabledDefined
	globalEnabledChanged := m.current.Enabled != cfg.Enabled
	if ambiguousEnabledLoosening || permissivenessLoosened(m.current, cfg, globalEnabledChanged) {
		now := time.Now()
		if !snapshotsEqual(m.pendingLoosening.snapshot, snap) || m.pendingLoosening.firstSeen.IsZero() {
			m.clearPendingLooseningLocked()
			m.pendingLoosening.snapshot = snap
			m.pendingLoosening.firstSeen = now
		}
		elapsed := now.Sub(m.pendingLoosening.firstSeen)
		if elapsed < m.horizon {
			// A closed manager holds the loosening unresolved rather than
			// arming a retry that would outlive its owner.
			if m.pendingLoosening.retry == nil && !m.closed {
				delay := m.horizon - elapsed
				m.retries.Add(1)
				m.pendingLoosening.retry = time.AfterFunc(delay, func() {
					defer m.retries.Done()
					_ = m.reload()
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
	if timer := m.pendingLoosening.retry; timer != nil {
		// Stop reporting true is the only case where the retry func is
		// guaranteed never to run, so it is the only case where this call owns
		// the tracking entry the func would otherwise release itself. Stop
		// reporting false means the func has already fired (possibly it is the
		// very call stack running this), and its own deferred Done covers it.
		if timer.Stop() {
			m.retries.Done()
		}
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
