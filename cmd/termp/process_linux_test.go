//go:build linux

package main

import (
	"os"
	"testing"
)

func TestLinuxIdentityMatches(t *testing.T) {
	const uid = uint32(1000)
	tests := []struct {
		name       string
		actualUID  uint32
		currentUID uint32
		actualPath string
		wantPath   string
		want       bool
	}{
		{name: "owner and canonical path match", actualUID: uid, currentUID: uid, actualPath: "/opt/termp", wantPath: "/opt/termp", want: true},
		{name: "owner mismatch", actualUID: uid + 1, currentUID: uid, actualPath: "/opt/termp", wantPath: "/opt/termp"},
		{name: "same basename different executable", actualUID: uid, currentUID: uid, actualPath: "/tmp/termp", wantPath: "/opt/termp"},
		{name: "missing target identity fails closed", actualUID: uid, currentUID: uid, actualPath: "", wantPath: "/opt/termp"},
		{name: "missing current identity fails closed", actualUID: uid, currentUID: uid, actualPath: "/opt/termp", wantPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxIdentityMatches(tt.actualUID, tt.currentUID, tt.actualPath, tt.wantPath); got != tt.want {
				t.Fatalf("linuxIdentityMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLinuxLiveAndStaleProcessLookup(t *testing.T) {
	if !processAlive(os.Getpid()) || !processLooksLikeTermp(os.Getpid()) {
		t.Fatal("live current process identity was not proven")
	}
	if processAlive(99999999) || processLooksLikeTermp(99999999) {
		t.Fatal("stale PID was accepted")
	}
}

func TestPidfdInfoMatchesPID(t *testing.T) {
	tests := []struct {
		name string
		data string
		pid  int
		want bool
	}{
		{name: "matching pidfd", data: "pos:\t0\nflags:\t02000002\nPid:\t42\n", pid: 42, want: true},
		{name: "reaped target", data: "Pid:\t-1\n", pid: 42},
		{name: "recycled pid", data: "Pid:\t41\n", pid: 42},
		{name: "missing identity", data: "flags:\t02000002\n", pid: 42},
		{name: "invalid requested pid", data: "Pid:\t42\n", pid: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pidfdInfoMatchesPID([]byte(tt.data), tt.pid); got != tt.want {
				t.Fatalf("pidfdInfoMatchesPID() = %v, want %v", got, tt.want)
			}
		})
	}
}
