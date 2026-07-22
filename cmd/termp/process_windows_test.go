//go:build windows

package main

import (
	"os"
	"testing"

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
