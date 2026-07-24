//go:build windows

package main

import (
	"errors"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsIdentityMatches(t *testing.T) {
	currentSID, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	otherSID, err := windows.StringToSid("S-1-5-21-1-2-3-1002")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name                    string
		actualSID, expectedSID  *windows.SID
		actualPath, currentPath string
		want                    bool
	}{
		{name: "owner and full path match", actualSID: currentSID, expectedSID: currentSID, actualPath: "C:\\Program Files\\termp.exe", currentPath: "C:\\Program Files\\termp.exe", want: true},
		{name: "path casing differences match", actualSID: currentSID, expectedSID: currentSID, actualPath: "c:\\program files\\TERMP.EXE", currentPath: "C:\\Program Files\\termp.exe", want: true},
		{name: "extended path prefix normalized", actualSID: currentSID, expectedSID: currentSID, actualPath: "\\\\?\\C:\\Program Files\\termp.exe", currentPath: "C:\\Program Files\\termp.exe", want: true},
		{name: "owner mismatch", actualSID: otherSID, expectedSID: currentSID, actualPath: "C:\\termp.exe", currentPath: "C:\\termp.exe"},
		{name: "same basename different executable", actualSID: currentSID, expectedSID: currentSID, actualPath: "C:\\Temp\\termp.exe", currentPath: "C:\\Program Files\\termp.exe"},
		{name: "missing SID fails closed", actualSID: nil, expectedSID: currentSID, actualPath: "C:\\termp.exe", currentPath: "C:\\termp.exe"},
		{name: "missing path fails closed", actualSID: currentSID, expectedSID: currentSID, actualPath: "", currentPath: "C:\\termp.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowsIdentityMatches(tt.actualSID, tt.expectedSID, tt.actualPath, tt.currentPath)
			if got != tt.want {
				t.Fatalf("windowsIdentityMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWindowsLiveAndStaleProcessLookup(t *testing.T) {
	if !processAlive(os.Getpid()) || !processLooksLikeTermp(os.Getpid()) {
		t.Fatal("live current process identity was not proven")
	}
	if processAlive(99999999) || processLooksLikeTermp(99999999) {
		t.Fatal("stale PID was accepted")
	}
}

func TestSignalTermpProcessFallsBackToTerminateWhenSetEventFails(t *testing.T) {
	oldOpenEvent := windowsOpenEvent
	oldSetEvent := windowsSetEvent
	oldOpenProcess := windowsOpenProcess
	oldCloseHandle := windowsCloseHandle
	oldTerminateProcess := windowsTerminateProcess
	oldValidate := validateWindowsProcessHandleForSignal
	t.Cleanup(func() {
		windowsOpenEvent = oldOpenEvent
		windowsSetEvent = oldSetEvent
		windowsOpenProcess = oldOpenProcess
		windowsCloseHandle = oldCloseHandle
		windowsTerminateProcess = oldTerminateProcess
		validateWindowsProcessHandleForSignal = oldValidate
	})

	const (
		eventHandle   windows.Handle = 101
		processHandle windows.Handle = 202
	)
	setEventErr := errors.New("set event failed")
	var openedProcess bool
	var terminated bool
	closed := make(map[windows.Handle]bool)

	windowsOpenEvent = func(uint32, bool, *uint16) (windows.Handle, error) {
		return eventHandle, nil
	}
	windowsSetEvent = func(handle windows.Handle) error {
		if handle != eventHandle {
			t.Fatalf("SetEvent handle = %v, want %v", handle, eventHandle)
		}
		return setEventErr
	}
	windowsOpenProcess = func(access uint32, inheritHandle bool, pid uint32) (windows.Handle, error) {
		openedProcess = true
		if pid != 1234 {
			t.Fatalf("OpenProcess pid = %d, want 1234", pid)
		}
		return processHandle, nil
	}
	validateWindowsProcessHandleForSignal = func(handle windows.Handle) error {
		if handle != processHandle {
			t.Fatalf("validate handle = %v, want %v", handle, processHandle)
		}
		return nil
	}
	windowsTerminateProcess = func(handle windows.Handle, exitCode uint32) error {
		if handle != processHandle {
			t.Fatalf("TerminateProcess handle = %v, want %v", handle, processHandle)
		}
		terminated = true
		return nil
	}
	windowsCloseHandle = func(handle windows.Handle) error {
		closed[handle] = true
		return nil
	}

	if err := signalTermpProcess(1234); err != nil {
		t.Fatalf("signalTermpProcess returned error: %v", err)
	}
	if !openedProcess || !terminated {
		t.Fatalf("fallback openedProcess=%t terminated=%t, want both true", openedProcess, terminated)
	}
	if !closed[eventHandle] || !closed[processHandle] {
		t.Fatalf("closed handles = %#v, want event and process handles closed", closed)
	}
}

func TestProcessImagePathTriesMaximumBuffer(t *testing.T) {
	var sizes []uint32
	path, err := processImagePathWithQuery(func(buffer *uint16, length *uint32) error {
		sizes = append(sizes, *length)
		if *length < 20000 {
			return windows.ERROR_INSUFFICIENT_BUFFER
		}
		encoded, encodeErr := windows.UTF16FromString(`C:\very-long\termp.exe`)
		if encodeErr != nil {
			return encodeErr
		}
		copy(unsafe.Slice(buffer, *length), encoded[:len(encoded)-1])
		*length = uint32(len(encoded) - 1)
		return nil
	})
	if err != nil || path != `C:\very-long\termp.exe` {
		t.Fatalf("processImagePathWithQuery() = %q, %v", path, err)
	}
	if got := sizes[len(sizes)-1]; got != 32768 {
		t.Fatalf("largest queried buffer = %d, want 32768 (all sizes: %v)", got, sizes)
	}
}
