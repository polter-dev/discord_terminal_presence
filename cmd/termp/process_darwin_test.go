//go:build darwin

package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinProcessStartedWithRelativePath(t *testing.T) {
	if os.Getenv("TERMP_RELATIVE_PROCESS_HELPER") == "1" {
		_, _ = os.Stdout.WriteString("ready\n")
		_ = os.Stdout.Sync()
		time.Sleep(time.Minute)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeExecutable, err := filepath.Rel(workingDir, executable)
	if err != nil || filepath.IsAbs(relativeExecutable) {
		t.Fatalf("relative executable = %q, %v", relativeExecutable, err)
	}
	cmd := exec.Command(relativeExecutable, "-test.run=TestDarwinProcessStartedWithRelativePath")
	cmd.Env = append(os.Environ(), "TERMP_RELATIVE_PROCESS_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if scanner := bufio.NewScanner(stdout); !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("relative helper did not become ready: %q, %v", scanner.Text(), scanner.Err())
	}
	if _, err := validatedDarwinIdentity(cmd.Process.Pid); err != nil {
		t.Fatalf("relative process identity was not proven: %v", err)
	}
}

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
