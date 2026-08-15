package detector

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	windowsTTYPathPrefix   = "win:hwnd:"
	windowsFocusStateLimit = 1024
	consoleProbeArg        = "--termp-internal-console-probe"
)

type windowsTTYResolver struct {
	consoleHWNDForPID func(int32) (hwnd uintptr, conPTY bool, err error)
	cache             *windowsConsoleProbeCache
}

type windowsConsoleProbeResult struct {
	hwnd   uintptr
	conPTY bool
	err    error
}

type windowsConsoleProbeCache struct {
	mu      sync.Mutex
	results map[int32]windowsConsoleProbeResult
}

func newWindowsTTYResolver(probe func(int32) (uintptr, bool, error)) windowsTTYResolver {
	return windowsTTYResolver{
		consoleHWNDForPID: probe,
		cache: &windowsConsoleProbeCache{
			results: make(map[int32]windowsConsoleProbeResult),
		},
	}
}

func consoleProbeRequested(args []string, envPresent bool) bool {
	return envPresent && len(args) == 2 && args[1] == consoleProbeArg
}

func consoleProbeExecutablePath(imagePath string) (string, error) {
	// The probe must run the real binary directly. A Scoop shim can create a
	// visible console for the child it launches even when the shim itself was
	// started with CREATE_NO_WINDOW (#508).
	return filepath.Abs(filepath.Join(filepath.Dir(imagePath), "termp.exe"))
}

// windowsConsoleWindow describes what GetConsoleWindow returned for a probed
// process, together with the observable state of that window. It is populated
// from Win32 in the probe child (tty_windows.go) but kept free of any platform
// types so the fail-open decision can be unit-tested on any host.
type windowsConsoleWindow struct {
	hwnd uintptr
	// exists is IsWindow(hwnd): false for a stale or torn-down handle.
	exists bool
	// visible is IsWindowVisible(hwnd). A real classic-terminal console window
	// carries WS_VISIBLE (true even when minimized); a headless ConPTY conhost's
	// pseudoconsole window is hidden.
	visible bool
}

// shouldConsoleFailOpen reports whether a probed console must be treated as the
// ConPTY / headless-conhost fail-open case (conPTY=true), so Resolve stays
// TTYUnknown and the tool is always featured and can never be mis-cleared by the
// idle clock.
//
// Fail open when:
//   - hwnd == 0 — the classic ConPTY case: the attached process has no console
//     window at all (the original, still-covered path); or
//   - the returned handle is not a real, visible top-level terminal window: it
//     does not exist, or it is not visible. A headless ConPTY conhost
//     (OpenPseudoConsole / `conhost.exe --headless` lineage) can return a
//     non-null *hidden* window handle on some Windows builds (#501); resolving
//     that handle would freeze the activity clock because the foreground
//     WindowsTerminal.exe window never shares its GA_ROOTOWNER, so idle-clear
//     eventually drops an actively-used session. Treating hidden/absent windows
//     as fail-open closes that gap.
//
// A real terminal console window (visible, existing) is not fail-open: it
// resolves normally so classic-console focus/idle detection keeps working.
func shouldConsoleFailOpen(w windowsConsoleWindow) bool {
	if w.hwnd == 0 {
		return true
	}
	if !w.exists {
		return true
	}
	return !w.visible
}

func selectConsolePeer(pids []uint32, ownPID uint32) (uint32, bool) {
	for _, pid := range pids {
		if pid != 0 && pid != ownPID {
			return pid, true
		}
	}
	return 0, false
}

func (r windowsTTYResolver) Resolve(pid int32) (TTYResolution, error) {
	if r.consoleHWNDForPID == nil {
		return TTYResolution{}, errors.New("windows console resolver unavailable")
	}
	var hwnd uintptr
	var conPTY bool
	var err error
	if r.cache == nil {
		hwnd, conPTY, err = r.consoleHWNDForPID(pid)
	} else {
		r.cache.mu.Lock()
		result, ok := r.cache.results[pid]
		if !ok {
			result.hwnd, result.conPTY, result.err = r.consoleHWNDForPID(pid)
			r.cache.results[pid] = result
		}
		r.cache.mu.Unlock()
		hwnd, conPTY, err = result.hwnd, result.conPTY, result.err
	}
	if err != nil {
		return TTYResolution{}, err
	}
	if conPTY {
		return TTYResolution{}, errors.New("windows console has no window handle")
	}
	if hwnd == 0 {
		return TTYResolution{}, errors.New("windows console window handle unavailable")
	}
	return TTYResolution{Path: fmt.Sprintf("%s%d", windowsTTYPathPrefix, hwnd)}, nil
}

type windowsTTYAtimeSource struct {
	foregroundWindow func() uintptr
	rootOwnerWindow  func(uintptr) uintptr
	windowExists     func(uintptr) bool
	lastInputMillis  func() (uint32, bool)
	now              func() time.Time

	focusMu    sync.Mutex
	focusState map[uintptr]windowsFocusObservation
}

type windowsFocusObservation struct {
	lastForeground time.Time
	lastSeen       time.Time
}

func (s *windowsTTYAtimeSource) Atime(path string) (time.Time, error) {
	return s.AtimeWithEpisode(path, time.Time{}, false)
}

func (s *windowsTTYAtimeSource) AtimeWithEpisode(path string, lastAtime time.Time, episodeExists bool) (time.Time, error) {
	atime, _, err := s.AtimeWithFocus(path, lastAtime, episodeExists)
	return atime, err
}

func (s *windowsTTYAtimeSource) AtimeWithFocus(path string, lastAtime time.Time, episodeExists bool) (time.Time, bool, error) {
	hwnd, err := parseWindowsTTYPath(path)
	if err != nil {
		return time.Time{}, false, err
	}
	if s.foregroundWindow == nil {
		return time.Time{}, false, errors.New("windows foreground window resolver unavailable")
	}
	if s.rootOwnerWindow == nil {
		return time.Time{}, false, errors.New("windows root owner window resolver unavailable")
	}
	if s.lastInputMillis == nil {
		return time.Time{}, false, errors.New("windows last input resolver unavailable")
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	foreground := s.foregroundWindow()
	if foreground == 0 {
		return time.Time{}, false, errors.New("windows foreground window unavailable")
	}
	ownerOf := func(window uintptr) uintptr {
		owner := s.rootOwnerWindow(window)
		if owner == 0 {
			return window
		}
		return owner
	}
	current := now()
	if ownerOf(foreground) != ownerOf(hwnd) {
		return s.observeFocus(hwnd, current, false, lastAtime, episodeExists), false, nil
	}
	s.observeFocus(hwnd, current, true, time.Time{}, false)
	idleMillis, ok := s.lastInputMillis()
	if !ok {
		return time.Time{}, false, errors.New("windows last input unavailable")
	}
	return current.Add(-time.Duration(idleMillis) * time.Millisecond), true, nil
}

func (s *windowsTTYAtimeSource) observeFocus(hwnd uintptr, current time.Time, focused bool, lastAtime time.Time, episodeExists bool) time.Time {
	s.focusMu.Lock()
	defer s.focusMu.Unlock()

	if s.focusState == nil {
		s.focusState = make(map[uintptr]windowsFocusObservation)
	}
	if s.windowExists != nil {
		for tracked := range s.focusState {
			if !s.windowExists(tracked) {
				delete(s.focusState, tracked)
			}
		}
	}

	observation, ok := s.focusState[hwnd]
	if !ok || focused {
		observation.lastForeground = current
		if !ok && !focused && episodeExists && !lastAtime.IsZero() {
			// Preserve the existing idle clock across daemon restarts. An
			// episode is exact to the matched tool process, not merely HWND.
			observation.lastForeground = lastAtime
		}
	}
	observation.lastSeen = current
	s.focusState[hwnd] = observation

	for len(s.focusState) > windowsFocusStateLimit {
		var (
			oldestHWND uintptr
			oldest     time.Time
		)
		for tracked, candidate := range s.focusState {
			if tracked == hwnd {
				continue
			}
			if oldestHWND == 0 || candidate.lastSeen.Before(oldest) {
				oldestHWND = tracked
				oldest = candidate.lastSeen
			}
		}
		if oldestHWND == 0 {
			break
		}
		delete(s.focusState, oldestHWND)
	}
	return observation.lastForeground
}

func parseWindowsTTYPath(path string) (uintptr, error) {
	raw, ok := strings.CutPrefix(path, windowsTTYPathPrefix)
	if !ok || raw == "" {
		return 0, errors.New("invalid windows tty path")
	}
	hwnd, err := strconv.ParseUint(raw, 10, 0)
	if err != nil || hwnd == 0 {
		return 0, errors.New("invalid windows console window handle")
	}
	return uintptr(hwnd), nil
}
