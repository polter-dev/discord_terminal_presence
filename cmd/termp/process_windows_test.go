//go:build windows

package main

import (
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
