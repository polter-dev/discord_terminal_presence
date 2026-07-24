package detector

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	windowsTTYPathPrefix = "win:hwnd:"
	windowsInactiveAge   = 24 * time.Hour
)

type windowsTTYResolver struct {
	consoleHWNDForPID func(int32) (hwnd uintptr, conPTY bool, err error)
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
	lastInputMillis  func() (uint32, bool)
	now              func() time.Time
}

func (s windowsTTYAtimeSource) Atime(path string) (time.Time, error) {
	hwnd, err := parseWindowsTTYPath(path)
	if err != nil {
		return time.Time{}, err
	}
	if s.foregroundWindow == nil {
		return time.Time{}, errors.New("windows foreground window resolver unavailable")
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
	current := now()
	if foreground != hwnd {
		return current.Add(-windowsInactiveAge), nil
	}
	idleMillis, ok := s.lastInputMillis()
	if !ok {
		return time.Time{}, errors.New("windows last input unavailable")
	}
	return current.Add(-time.Duration(idleMillis) * time.Millisecond), nil
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
