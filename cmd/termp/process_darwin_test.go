//go:build darwin

package main

import (
	"os"
	"testing"
)

func TestDarwinIdentityMatches(t *testing.T) {
	const uid = uint32(501)
	tests := []struct {
		name                    string
		kinfoUID, currentUID    uint32
		actualPath, currentPath string
		want                    bool
	}{
		{name: "owner and full path match", kinfoUID: uid, currentUID: uid, actualPath: "/Applications/termp", currentPath: "/Applications/termp", want: true},
		{name: "owner mismatch", kinfoUID: uid + 1, currentUID: uid, actualPath: "/Applications/termp", currentPath: "/Applications/termp"},
		{name: "same basename different executable", kinfoUID: uid, currentUID: uid, actualPath: "/tmp/termp", currentPath: "/Applications/termp"},
		{name: "missing image fails closed", kinfoUID: uid, currentUID: uid, actualPath: "", currentPath: "/Applications/termp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := darwinIdentityMatches(tt.kinfoUID, tt.currentUID, tt.actualPath, tt.currentPath)
			if got != tt.want {
				t.Fatalf("darwinIdentityMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDarwinExecutableFromProcArgs(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
		ok   bool
	}{
		{name: "image path", data: append([]byte{1, 0, 0, 0}, []byte("/opt/termp\x00arg\x00")...), want: "/opt/termp", ok: true},
		{name: "missing data", data: []byte{1, 0, 0, 0}},
		{name: "empty path", data: []byte{1, 0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := darwinExecutableFromProcArgs(tt.data)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("darwinExecutableFromProcArgs() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDarwinStableProcessIdentity(t *testing.T) {
	base := darwinProcessIdentity{uid: 501, path: "/Applications/termp", startSec: 10, startUsec: 20}
	tests := []struct {
		name  string
		other darwinProcessIdentity
		want  bool
	}{
		{name: "same process", other: base, want: true},
		{name: "recycled pid", other: darwinProcessIdentity{uid: 501, path: "/Applications/termp", startSec: 11, startUsec: 20}},
		{name: "changed path", other: darwinProcessIdentity{uid: 501, path: "/tmp/termp", startSec: 10, startUsec: 20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameDarwinProcess(base, tt.other); got != tt.want {
				t.Fatalf("sameDarwinProcess() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDarwinLiveAndStaleProcessLookup(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process was not alive")
	}
	if _, err := validatedDarwinIdentity(os.Getpid()); err != nil {
		t.Fatalf("live current process identity was not proven: %v", err)
	}
	if processAlive(99999999) || processLooksLikeTermp(99999999) {
		t.Fatal("stale PID was accepted")
	}
}
