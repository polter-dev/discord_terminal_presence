//go:build darwin

package detector

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

const darwinIdentityHelperEnv = "TERMP_DARWIN_IDENTITY_HELPER"

// TestDarwinUIDPrefilterDoesNotDropOwnedProcesses proves the early effective-
// UID filter only drops processes that the real authoritative resolver does
// not identify as owned. Processes that exit during the re-check are ignored:
// the resolver fails closed for them, so they cannot become presence.
func TestDarwinUIDPrefilterDoesNotDropOwnedProcesses(t *testing.T) {
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		t.Fatal(err)
	}

	resolver := newSystemOwnerResolver()
	euid := uint32(os.Geteuid())
	for i := range kprocs {
		kproc := &kprocs[i]
		if kproc.Eproc.Ucred.Uid == euid {
			continue
		}
		millis := kproc.Proc.P_starttime.Sec*1000 + int64(kproc.Proc.P_starttime.Usec)/1000
		owned, err := resolver.Owned(int32(kproc.Proc.P_pid), time.UnixMilli(millis))
		if err != nil {
			continue
		}
		if owned {
			t.Fatalf("prefilter dropped pid %d with uid %d, but unixOwnerResolver.Owned reported it owned", kproc.Proc.P_pid, kproc.Eproc.Ucred.Uid)
		}
	}
}

// TestDarwinBulkIdentitiesMatchGopsutil proves the bulk path preserves every
// identity field for processes present in both live snapshots. It also keeps
// adversarial helper processes alive throughout both listings so long-name,
// quoting, and Unicode behavior cannot be skipped as an exit race.
func TestDarwinBulkIdentitiesMatchGopsutil(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{
			name: "long-process-name-over-fifteen",
			args: []string{"argument containing spaces", `outer "double 'nested' quotes"`},
		},
		{
			name: "exactly15charsx",
			args: []string{"fifteen-character boundary"},
		},
		{
			name: "unicode-😀-process",
			args: []string{"emoji binary name"},
		},
	}
	if got := len(cases[1].name); got != 15 {
		t.Fatalf("boundary fixture length = %d, want exactly 15 bytes", got)
	}

	helpers := make([]darwinIdentityHelper, 0, len(cases))
	for _, tc := range cases {
		helpers = append(helpers, startDarwinIdentityHelper(t, tc.name, tc.args))
	}

	wantList, err := listGopsutilIdentitiesForDarwinTest()
	if err != nil {
		t.Fatal(err)
	}
	gotList, err := listDarwinIdentities()
	if err != nil {
		t.Fatal(err)
	}
	wantByPID := identitiesByPID(wantList)
	gotByPID := identitiesByPID(gotList)

	compared := 0
	for pid, want := range wantByPID {
		got, ok := gotByPID[pid]
		if !ok {
			continue
		}
		compared++
		if identitiesEqual(want, got) {
			continue
		}
		// A live host process can exit or be replaced between the two complete
		// listings. Retry that pid against a fresh kinfo record; only assert a
		// mismatch when the same process instance remains available.
		freshWant, freshGot, stable := freshDarwinIdentityPair(pid)
		if !stable {
			continue
		}
		assertIdentitiesEqual(t, pid, freshWant, freshGot)
	}
	if compared == 0 {
		t.Fatal("bulk and gopsutil listings had no common pids")
	}

	for _, helper := range helpers {
		want, ok := wantByPID[helper.pid]
		if !ok {
			t.Fatalf("gopsutil listing omitted live helper pid %d (%q)", helper.pid, helper.name)
		}
		got, ok := gotByPID[helper.pid]
		if !ok {
			t.Fatalf("bulk listing omitted live helper pid %d (%q)", helper.pid, helper.name)
		}
		assertIdentitiesEqual(t, helper.pid, want, got)
		if got.Name != helper.name {
			t.Errorf("pid %d Name = %q, want helper basename %q", helper.pid, got.Name, helper.name)
		}
		if got.Argv0 != helper.path {
			t.Errorf("pid %d Argv0 = %q, want %q", helper.pid, got.Argv0, helper.path)
		}
		for _, arg := range helper.args {
			if !slices.Contains(got.Argv, arg) {
				t.Errorf("pid %d Argv = %#v, want adversarial argument %q", helper.pid, got.Argv, arg)
			}
		}
		if got.Cmdline != strings.Join(got.Argv, " ") {
			t.Errorf("pid %d Cmdline = %q, want argv joined with spaces %q", helper.pid, got.Cmdline, strings.Join(got.Argv, " "))
		}
	}
}

// TestDarwinIdentityHelperProcess is re-executed under adversarial basenames.
// It stays alive until the parent closes stdin so both live listings observe
// the same process instance.
func TestDarwinIdentityHelperProcess(t *testing.T) {
	if os.Getenv(darwinIdentityHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type darwinIdentityHelper struct {
	pid  int32
	name string
	path string
	args []string
}

func startDarwinIdentityHelper(t *testing.T, name string, args []string) darwinIdentityHelper {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	copyExecutable(t, executable, path)

	commandArgs := append([]string{"-test.run=^TestDarwinIdentityHelperProcess$"}, args...)
	cmd := exec.Command(path, commandArgs...)
	cmd.Env = append(os.Environ(), darwinIdentityHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("helper %q did not exit cleanly: %v", name, err)
		}
	})
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read helper %q readiness: %v", name, err)
	}
	if ready != "ready\n" {
		t.Fatalf("helper %q readiness = %q, want %q", name, ready, "ready\\n")
	}
	return darwinIdentityHelper{
		pid:  int32(cmd.Process.Pid),
		name: name,
		path: path,
		args: args,
	}
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func listGopsutilIdentitiesForDarwinTest() ([]Process, error) {
	processes, err := psprocess.Processes()
	if err != nil {
		return nil, err
	}
	out := make([]Process, 0, len(processes))
	for _, proc := range processes {
		process := processIdentity(proc)
		if process.Name == "" && process.Exe == "" && process.Cmdline == "" && process.Argv0 == "" {
			continue
		}
		out = append(out, process)
	}
	return out, nil
}

func identitiesByPID(processes []Process) map[int32]Process {
	byPID := make(map[int32]Process, len(processes))
	for _, process := range processes {
		byPID[process.Pid] = process
	}
	return byPID
}

func freshDarwinIdentityPair(pid int32) (Process, Process, bool) {
	kproc, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil || kproc.Eproc.Ucred.Uid != uint32(os.Geteuid()) {
		return Process{}, Process{}, false
	}
	want := processIdentity(&psprocess.Process{Pid: pid})
	got := darwinProcessIdentity(kproc)
	verified, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil || verified.Proc.P_starttime != kproc.Proc.P_starttime {
		return Process{}, Process{}, false
	}
	return want, got, true
}

func identitiesEqual(want, got Process) bool {
	return want.Name == got.Name &&
		want.Exe == got.Exe &&
		want.Argv0 == got.Argv0 &&
		reflect.DeepEqual(want.Argv, got.Argv) &&
		want.Cmdline == got.Cmdline &&
		want.CreateTime.Equal(got.CreateTime)
}

func assertIdentitiesEqual(t *testing.T, pid int32, want, got Process) {
	t.Helper()
	if want.Name != got.Name {
		t.Errorf("pid %d Name = %q, want %q", pid, got.Name, want.Name)
	}
	if want.Exe != got.Exe {
		t.Errorf("pid %d Exe = %q, want %q", pid, got.Exe, want.Exe)
	}
	if want.Argv0 != got.Argv0 {
		t.Errorf("pid %d Argv0 = %q, want %q", pid, got.Argv0, want.Argv0)
	}
	if !reflect.DeepEqual(want.Argv, got.Argv) {
		t.Errorf("pid %d Argv = %#v, want %#v", pid, got.Argv, want.Argv)
	}
	if want.Cmdline != got.Cmdline {
		t.Errorf("pid %d Cmdline = %q, want %q", pid, got.Cmdline, want.Cmdline)
	}
	if !want.CreateTime.Equal(got.CreateTime) {
		t.Errorf("pid %d CreateTime = %v, want %v", pid, got.CreateTime, want.CreateTime)
	}
}
