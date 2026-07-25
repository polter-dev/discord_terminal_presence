package detector

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestWindowsTTYAtimeOwnerBasedForegroundComparison(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	const (
		h1 uintptr = 100
		w  uintptr = 300
	)
	tests := []struct {
		name       string
		hwnd       uintptr
		foreground uintptr
		owners     map[uintptr]uintptr
		wantAge    time.Duration
	}{
		{"ConPTY focused terminal is recent", h1, w, map[uintptr]uintptr{h1: w, w: w}, 250 * time.Millisecond},
		{"classic console focused is recent regression guard for issue 183", h1, h1, map[uintptr]uintptr{h1: h1}, 250 * time.Millisecond},
		{"zero owner lookup falls back to raw handle", h1, h1, map[uintptr]uintptr{}, 250 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return tt.foreground },
				rootOwnerWindow:  func(hwnd uintptr) uintptr { return tt.owners[hwnd] },
				lastInputMillis:  func() (uint32, bool) { return 250, true },
				now:              func() time.Time { return base },
			}
			atime, err := source.Atime("win:hwnd:" + strconv.FormatUint(uint64(tt.hwnd), 10))
			if err != nil {
				t.Fatalf("Atime returned error: %v", err)
			}
			if age := base.Sub(atime); age != tt.wantAge {
				t.Fatalf("age = %v, want %v", age, tt.wantAge)
			}
		})
	}
}

func TestWindowsTTYAtimeLossOfFocusStartsIdleClock(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	current := base
	foreground := uintptr(300)
	owners := map[uintptr]uintptr{100: 300, 300: 300, 400: 400}
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return foreground },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return owners[hwnd] },
		lastInputMillis:  func() (uint32, bool) { return 0, true },
		now:              func() time.Time { return current },
	}

	if _, err := source.Atime("win:hwnd:100"); err != nil {
		t.Fatalf("focused Atime returned error: %v", err)
	}
	current = current.Add(3 * time.Second)
	foreground = 400
	atime, err := source.Atime("win:hwnd:100")
	if err != nil {
		t.Fatalf("unfocused Atime returned error: %v", err)
	}
	if age := current.Sub(atime); age != 3*time.Second {
		t.Fatalf("age just after focus loss = %v, want %v", age, 3*time.Second)
	}
}

func TestWindowsTTYAtimeFirstUnfocusedObservationGetsFullGracePeriod(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return 400 },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
		lastInputMillis: func() (uint32, bool) {
			t.Fatal("lastInputMillis called for unfocused window")
			return 0, false
		},
		now: func() time.Time { return base },
	}

	atime, err := source.Atime("win:hwnd:100")
	if err != nil {
		t.Fatalf("Atime returned error: %v", err)
	}
	if !atime.Equal(base) {
		t.Fatalf("Atime = %v, want first observation time %v", atime, base)
	}
}

func TestWindowsTTYAtimeClassicConhostOwnerComparisonRetainsFocusTime(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	foreground := uintptr(100)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return foreground },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
		lastInputMillis:  func() (uint32, bool) { return 0, true },
		now:              func() time.Time { return base },
	}

	if _, err := source.Atime("win:hwnd:100"); err != nil {
		t.Fatalf("focused Atime returned error: %v", err)
	}
	foreground = 400
	atime, err := source.Atime("win:hwnd:100")
	if err != nil {
		t.Fatalf("unfocused Atime returned error: %v", err)
	}
	if !atime.Equal(base) {
		t.Fatalf("Atime = %v, want last classic-conhost focus time %v", atime, base)
	}
}

func TestWindowsTTYAtimeUnfocusedPastIdleTimeoutClearsPresence(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	const timeout = 20 * time.Minute
	clock := &fakeClock{now: base}
	foreground := uintptr(100)
	source := &windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return foreground },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
		lastInputMillis:  func() (uint32, bool) { return 0, true },
		now:              clock.Now,
	}
	selector := NewSelector(testRegistry(t), Config{
		IdleClearTimeout:       timeout,
		CorroborateIdleWithCPU: true,
		ActivitySwitching:      true,
	}, clock)
	enricher := presenceEnricher(
		fakeTTYResolver{resolutions: map[int32]TTYResolution{
			1: {Path: "win:hwnd:100"},
		}},
		fakeTmuxSnapshot{},
		source,
	)
	process := Process{
		Pid:        1,
		Name:       "claude",
		CreateTime: base.Add(-time.Hour),
		CPUTime:    10,
	}

	if detection := selector.SelectWithEnricher([]Process{process}, enricher); detection.None {
		t.Fatal("focused process should be eligible")
	}
	foreground = 400
	clock.Advance(timeout - time.Second)
	if detection := selector.SelectWithEnricher([]Process{process}, enricher); detection.None {
		t.Fatal("recently unfocused process should remain eligible during grace period")
	}
	clock.Advance(2 * time.Second)
	if detection := selector.SelectWithEnricher([]Process{process}, enricher); !detection.None {
		t.Fatalf("detection = %#v, want none after idle timeout", detection)
	}
}

func TestWindowsTTYAtimeKnownLimitationSharedOwnerTreatsBothTabsAsActive(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	const (
		h1 uintptr = 100
		h2 uintptr = 200
		w  uintptr = 300
	)
	owners := map[uintptr]uintptr{h1: w, h2: w, w: w}
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return w },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return owners[hwnd] },
		lastInputMillis:  func() (uint32, bool) { return 250, true },
		now:              func() time.Time { return base },
	}

	for _, hwnd := range []uintptr{h1, h2} {
		atime, err := source.Atime("win:hwnd:" + strconv.FormatUint(uint64(hwnd), 10))
		if err != nil {
			t.Fatalf("Atime(%d) returned error: %v", hwnd, err)
		}
		if age := base.Sub(atime); age != 250*time.Millisecond {
			t.Fatalf("Atime(%d) age = %v, want %v", hwnd, age, 250*time.Millisecond)
		}
	}
}

func TestWindowsTTYAtimeForegroundLongIdle(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return 1234 },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
		lastInputMillis:  func() (uint32, bool) { return uint32((30 * time.Minute).Milliseconds()), true },
		now:              func() time.Time { return base },
	}

	atime, err := source.Atime("win:hwnd:1234")
	if err != nil {
		t.Fatalf("Atime returned error: %v", err)
	}
	if age := base.Sub(atime); age != 30*time.Minute {
		t.Fatalf("age = %v, want %v", age, 30*time.Minute)
	}
}

func TestWindowsTTYAtimeFailuresFailOpen(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		source *windowsTTYAtimeSource
		path   string
	}{
		{
			name: "bad path",
			source: &windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 1234 },
				rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:bad:1234",
		},
		{
			name: "no foreground",
			source: &windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 0 },
				rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:hwnd:1234",
		},
		{
			name: "no root owner resolver",
			source: &windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 1234 },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:hwnd:1234",
		},
		{
			name: "last input failure",
			source: &windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 1234 },
				rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
				lastInputMillis:  func() (uint32, bool) { return 0, false },
				now:              func() time.Time { return base },
			},
			path: "win:hwnd:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.source.Atime(tt.path); err == nil {
				t.Fatal("Atime returned nil error")
			}
		})
	}
}

func TestWindowsTTYAtimeFocusStateIsPrunedAndBounded(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	current := base
	alive := make(map[uintptr]bool)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return 999999 },
		rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
		windowExists:     func(hwnd uintptr) bool { return alive[hwnd] },
		lastInputMillis:  func() (uint32, bool) { return 0, true },
		now:              func() time.Time { return current },
	}

	for hwnd := uintptr(1); hwnd <= windowsFocusStateLimit+1; hwnd++ {
		alive[hwnd] = true
		current = current.Add(time.Millisecond)
		path := "win:hwnd:" + strconv.FormatUint(uint64(hwnd), 10)
		if _, err := source.Atime(path); err != nil {
			t.Fatalf("Atime(%d) returned error: %v", hwnd, err)
		}
	}
	if got := len(source.focusState); got != windowsFocusStateLimit {
		t.Fatalf("focus state size = %d, want hard limit %d", got, windowsFocusStateLimit)
	}

	dead := uintptr(windowsFocusStateLimit + 1)
	alive[dead] = false
	if _, err := source.Atime("win:hwnd:2"); err != nil {
		t.Fatalf("Atime after window removal returned error: %v", err)
	}
	if _, ok := source.focusState[dead]; ok {
		t.Fatalf("focus state retained nonexistent window %d", dead)
	}
}

func TestWindowsTTYResolveMapsHWND(t *testing.T) {
	resolver := windowsTTYResolver{
		consoleHWNDForPID: func(pid int32) (uintptr, bool, error) {
			if pid != 42 {
				t.Fatalf("pid = %d, want 42", pid)
			}
			return 1234, false, nil
		},
	}

	resolved, err := resolver.Resolve(42)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.NoTTY {
		t.Fatal("Resolve returned NoTTY")
	}
	if resolved.Path != "win:hwnd:1234" {
		t.Fatalf("Path = %q, want %q", resolved.Path, "win:hwnd:1234")
	}
}

func TestWindowsTTYResolveConPTYFailsOpen(t *testing.T) {
	resolver := windowsTTYResolver{
		consoleHWNDForPID: func(int32) (uintptr, bool, error) {
			return 0, true, nil
		},
	}

	resolved, err := resolver.Resolve(42)
	if err == nil {
		t.Fatal("Resolve returned nil error")
	}
	if resolved.NoTTY {
		t.Fatal("Resolve returned NoTTY")
	}
}

func TestWindowsTTYResolveSyscallFailureFailsOpen(t *testing.T) {
	wantErr := errors.New("attach failed")
	resolver := windowsTTYResolver{
		consoleHWNDForPID: func(int32) (uintptr, bool, error) {
			return 0, false, wantErr
		},
	}

	resolved, err := resolver.Resolve(42)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Resolve error = %v, want %v", err, wantErr)
	}
	if resolved.NoTTY {
		t.Fatal("Resolve returned NoTTY")
	}
}

func TestSelectConsolePeerExcludesCurrentProcess(t *testing.T) {
	pid, ok := selectConsolePeer([]uint32{0, 42, 99}, 42)
	if !ok || pid != 99 {
		t.Fatalf("peer = (%d, %t), want (99, true)", pid, ok)
	}
	if pid, ok := selectConsolePeer([]uint32{0, 42}, 42); ok || pid != 0 {
		t.Fatalf("peer = (%d, %t), want (0, false)", pid, ok)
	}
}
