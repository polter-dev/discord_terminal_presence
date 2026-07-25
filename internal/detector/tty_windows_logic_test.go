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
		x  uintptr = 400
	)
	tests := []struct {
		name       string
		hwnd       uintptr
		foreground uintptr
		owners     map[uintptr]uintptr
		wantAge    time.Duration
	}{
		{"ConPTY focused terminal is recent", h1, w, map[uintptr]uintptr{h1: w, w: w}, 250 * time.Millisecond},
		{"ConPTY unfocused terminal is old", h1, x, map[uintptr]uintptr{h1: w, w: w, x: x}, windowsInactiveAge},
		{"classic console focused is recent regression guard for issue 183", h1, h1, map[uintptr]uintptr{h1: h1}, 250 * time.Millisecond},
		{"classic console unfocused is old", h1, x, map[uintptr]uintptr{h1: h1, x: x}, windowsInactiveAge},
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
		source windowsTTYAtimeSource
		path   string
	}{
		{
			name: "bad path",
			source: windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 1234 },
				rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:bad:1234",
		},
		{
			name: "no foreground",
			source: windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 0 },
				rootOwnerWindow:  func(hwnd uintptr) uintptr { return hwnd },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:hwnd:1234",
		},
		{
			name: "no root owner resolver",
			source: windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 1234 },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:hwnd:1234",
		},
		{
			name: "last input failure",
			source: windowsTTYAtimeSource{
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
