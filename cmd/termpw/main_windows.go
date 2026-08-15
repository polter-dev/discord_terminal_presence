//go:build windows

// Command termpw is the Windows-only companion launcher for the termp autostart
// task. It is linked with -H=windowsgui (the pythonw pattern) so it has no
// console of its own, launches the real `termp.exe start --foreground` daemon
// with daemon-owned logging as a child with no visible console window, waits for
// the daemon's whole lifetime, and propagates the daemon's exit status.
//
// Staying alive for the daemon's lifetime is deliberate: Task Scheduler keeps
// owning this process, so RestartOnFailure still triggers on a real crash and
// `schtasks /End` still stops the tree. A fire-and-forget launcher would
// silently degrade into the rejected "detach and exit" option.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// createNoWindow runs the console-subsystem daemon without allocating a visible
// console window. Because this launcher has no console of its own, Windows would
// otherwise create a new console (and a visible window) for the child — exactly
// the window this launcher exists to eliminate (issue #473).
const createNoWindow = 0x08000000

func main() {
	os.Exit(run())
}

func run() int {
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	// The daemon is a sibling in the same install directory. Resolve it by an
	// absolute path so the child cannot be picked up from PATH or the working
	// directory.
	daemon := filepath.Join(filepath.Dir(self), "termp.exe")

	cmd := exec.Command(daemon, launcherDaemonArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return 1
	}
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if code := exitErr.ExitCode(); code >= 0 {
				return code
			}
		}
		return 1
	}
	return 0
}
