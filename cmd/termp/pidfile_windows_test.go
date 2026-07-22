//go:build windows

package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func foreignOwnerFileInfo(_ os.FileInfo, _ uint32) (os.FileInfo, bool) {
	return nil, false
}

func TestWindowsPIDFileOwnerMatches(t *testing.T) {
	current, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	other, err := windows.StringToSid("S-1-5-21-1-2-3-1002")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name           string
		owner, current *windows.SID
		want           bool
	}{
		{name: "owner match", owner: current, current: current, want: true},
		{name: "owner mismatch", owner: other, current: current},
		{name: "unknown owner fails closed", owner: nil, current: current},
		{name: "unknown current user fails closed", owner: current, current: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pidFileOwnerMatches(tt.owner, tt.current); got != tt.want {
				t.Fatalf("pidFileOwnerMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWindowsPIDFileAttributes(t *testing.T) {
	tests := []struct {
		name       string
		attributes uint32
		want       bool
	}{
		{name: "regular file", attributes: windows.FILE_ATTRIBUTE_NORMAL, want: true},
		{name: "reparse point rejected", attributes: windows.FILE_ATTRIBUTE_REPARSE_POINT},
		{name: "reparse point with other flags rejected", attributes: windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_ARCHIVE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pidFileAttributesSafe(tt.attributes); got != tt.want {
				t.Fatalf("pidFileAttributesSafe() = %v, want %v", got, tt.want)
			}
		})
	}
}
