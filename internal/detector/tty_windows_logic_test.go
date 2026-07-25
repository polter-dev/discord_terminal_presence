package detector

import (
	"errors"
	"testing"
	"time"
)

func TestWindowsTTYAtimeForegroundRecentInput(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return 1234 },
		lastInputMillis:  func() (uint32, bool) { return 250, true },
		now:              func() time.Time { return base },
	}

	atime, err := source.Atime("win:hwnd:1234")
	if err != nil {
		t.Fatalf("Atime returned error: %v", err)
	}
	want := base.Add(-250 * time.Millisecond)
	if !atime.Equal(want) {
		t.Fatalf("Atime = %v, want %v", atime, want)
	}
}

func TestWindowsTTYAtimeForegroundLongIdle(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return 1234 },
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

func TestWindowsTTYAtimeBackgroundWindowIsOld(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	source := windowsTTYAtimeSource{
		foregroundWindow: func() uintptr { return 9999 },
		lastInputMillis: func() (uint32, bool) {
			t.Fatal("lastInputMillis should not be called for a background window")
			return 0, false
		},
		now: func() time.Time { return base },
	}

	atime, err := source.Atime("win:hwnd:1234")
	if err != nil {
		t.Fatalf("Atime returned error: %v", err)
	}
	if age := base.Sub(atime); age != windowsInactiveAge {
		t.Fatalf("age = %v, want %v", age, windowsInactiveAge)
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
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:bad:1234",
		},
		{
			name: "no foreground",
			source: windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 0 },
				lastInputMillis:  func() (uint32, bool) { return 0, true },
				now:              func() time.Time { return base },
			},
			path: "win:hwnd:1234",
		},
		{
			name: "last input failure",
			source: windowsTTYAtimeSource{
				foregroundWindow: func() uintptr { return 1234 },
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
