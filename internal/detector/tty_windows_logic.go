package detector

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	windowsTTYPathPrefix   = "win:hwnd:"
	windowsFocusStateLimit = 1024
)

type windowsTTYResolver struct {
	consoleHWNDForPID func(int32) (hwnd uintptr, conPTY bool, err error)
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
	hwnd, conPTY, err := r.consoleHWNDForPID(pid)
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
	hwnd, err := parseWindowsTTYPath(path)
	if err != nil {
		return time.Time{}, err
	}
	if s.foregroundWindow == nil {
		return time.Time{}, errors.New("windows foreground window resolver unavailable")
	}
	if s.rootOwnerWindow == nil {
		return time.Time{}, errors.New("windows root owner window resolver unavailable")
	}
	if s.lastInputMillis == nil {
		return time.Time{}, errors.New("windows last input resolver unavailable")
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	foreground := s.foregroundWindow()
	if foreground == 0 {
		return time.Time{}, errors.New("windows foreground window unavailable")
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
		return s.observeFocus(hwnd, current, false), nil
	}
	s.observeFocus(hwnd, current, true)
	idleMillis, ok := s.lastInputMillis()
	if !ok {
		return time.Time{}, errors.New("windows last input unavailable")
	}
	return current.Add(-time.Duration(idleMillis) * time.Millisecond), nil
}

func (s *windowsTTYAtimeSource) observeFocus(hwnd uintptr, current time.Time, focused bool) time.Time {
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
		// An unseen background window gets a full grace period: there is no
		// evidence that it lost focus before this observation.
		observation.lastForeground = current
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
