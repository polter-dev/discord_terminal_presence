//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// fakeDaemonPIDFileEnv makes this test binary, copied beside the launcher as
// termp.exe, act as the daemon: it records its PID and waits to be killed.
const fakeDaemonPIDFileEnv = "TERMPW_TEST_FAKE_DAEMON_PID_FILE"

func TestMain(m *testing.M) {
	if path := os.Getenv(fakeDaemonPIDFileEnv); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		// Bounded so an orphan left by a failing run does not outlive CI for long.
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Task Scheduler's End terminates only the task's own process, termpw.exe, the
// same way Process.Kill does. The daemon must not survive it (#662).
func TestTerminatingLauncherStopsDaemon(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "termpw.exe")
	if output, err := exec.Command("go", "build", "-ldflags=-H=windowsgui", "-o", launcher, ".").CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, output)
	}
	copyFile(t, testExecutable(t), filepath.Join(dir, "termp.exe"))

	pidFile := filepath.Join(dir, "daemon.pid")
	cmd := exec.Command(launcher)
	cmd.Env = append(os.Environ(), fakeDaemonPIDFileEnv+"="+pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	pid := waitForPID(t, pidFile)
	daemon, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open daemon pid %d: %v", pid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(daemon, 1)
		_, _ = windows.WaitForSingleObject(daemon, 5_000) // release termp.exe before TempDir cleanup
		_ = windows.CloseHandle(daemon)
	})
	if event, _ := windows.WaitForSingleObject(daemon, 0); event == windows.WAIT_OBJECT_0 {
		t.Fatalf("daemon pid %d exited while the launcher was still running", pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("terminate launcher: %v", err)
	}
	_ = cmd.Wait()

	event, err := windows.WaitForSingleObject(daemon, 10_000)
	if err != nil {
		t.Fatalf("wait for daemon pid %d: %v", pid, err)
	}
	if event != windows.WAIT_OBJECT_0 {
		t.Fatalf("daemon pid %d is still running 10s after its launcher was terminated", pid)
	}
}

func testExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	return exe
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy %s: %v", dst, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", dst, err)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fake daemon never wrote %s", path)
	return 0
}
