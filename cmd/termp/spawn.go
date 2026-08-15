package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const (
	detachedChildFlag     = "internal-detached-child"
	daemonLogFlag         = "internal-daemon-log"
	autostartFallbackFlag = "internal-autostart"
)

const (
	autostartConsoleTitle  = "Terminal Presence daemon"
	autostartConsoleBanner = "termp daemon: closing this window stops Discord presence; run `termp stop` to exit cleanly"
)

const (
	detachedStartTimeout      = 2 * time.Second
	detachedStartPollInterval = 25 * time.Millisecond

	// detachedStartStabilityWindow is how long waitForDetachedStart keeps
	// confirming a detached child after it first proves PID-file ownership,
	// before reporting start as successful. A child can still error out of
	// run()'s initialization (newDetectionRuntime, detector.New,
	// presence.NewWriter, ...) after the PID file is published but before
	// steady state, and that failure removes the PID file via the deferred
	// removePIDIfOwned in start() — this window exists to catch exactly
	// that instead of reporting success the instant ownership is first
	// observed (issue #490). It deliberately stays well under
	// detachedStartTimeout, and is bounded by whatever timeout budget
	// remains, so a successful start's added latency is at most a few
	// hundred milliseconds — `start` still does not wait for the presence
	// loop or first detector scan to run.
	detachedStartStabilityWindow = 400 * time.Millisecond
)

func detachedChildArgs(enableVerbose bool) []string {
	args := []string{"start", "--" + detachedChildFlag}
	if enableVerbose {
		args = append(args, "--verbose")
	}
	return args
}

func spawnDetachedStart(enableVerbose bool) (int, string, error) {
	executable, err := detachedExecutable()
	if err != nil {
		return 0, "", fmt.Errorf("resolve termp executable: %w", err)
	}
	logPath, err := detachedLogPath()
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, "", fmt.Errorf("create detached daemon log directory: %w", err)
	}
	nullFile, err := os.Open(os.DevNull)
	if err != nil {
		return 0, "", fmt.Errorf("open null input: %w", err)
	}
	defer nullFile.Close()
	panicLog, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("open detached daemon panic log: %w", err)
	}

	command := exec.Command(executable, detachedChildArgs(enableVerbose)...)
	command.Stdin = nullFile
	command.Stdout = nullFile
	command.Stderr = panicLog
	if err := startDetachedProcess(command); err != nil {
		_ = panicLog.Close()
		return 0, "", fmt.Errorf("start detached daemon: %w", err)
	}
	_ = panicLog.Close()
	pid := command.Process.Pid
	_ = command.Process.Release()
	if err := waitForDetachedStart(pidFilePath(), pid, detachedStartTimeout, detachedStartPollInterval, detachedStartStabilityWindow, readPID, processAlive, processLooksLikeTermp, time.Sleep); err != nil {
		return 0, "", fmt.Errorf("%w; logs: %s", err, logPath)
	}
	return pid, logPath, nil
}

// waitForDetachedStart confirms a detached child owns the PID file, then —
// when stabilityWindow > 0 — keeps confirming it for that long (bounded by
// whatever of timeout remains) before reporting success, so a child that
// dies during run()'s initialization right after publishing the PID file is
// reported as a failed start rather than a successful one (issue #490).
// Passing a zero stabilityWindow preserves the original "return the instant
// ownership is observed" behavior.
func waitForDetachedStart(path string, childPID int, timeout, pollInterval, stabilityWindow time.Duration, read func(string) (int, error), alive, looksLikeTermp func(int) bool, sleep func(time.Duration)) error {
	var lastReadErr error
	for waited := time.Duration(0); ; {
		ownerPID, err := read(path)
		if err == nil {
			if ownerPID != childPID {
				if alive(ownerPID) && looksLikeTermp(ownerPID) {
					return fmt.Errorf("daemon PID file is owned by pid %d instead of spawned pid %d", ownerPID, childPID)
				}
				if !alive(childPID) {
					return fmt.Errorf("detached daemon pid %d exited before owning the PID file", childPID)
				}
			}
			if ownerPID == childPID && !alive(childPID) {
				return fmt.Errorf("detached daemon pid %d exited during startup", childPID)
			}
			if ownerPID == childPID && looksLikeTermp(childPID) {
				remaining := timeout - waited
				return confirmDetachedStartStability(path, childPID, remaining, pollInterval, stabilityWindow, read, alive, sleep)
			}
		} else {
			lastReadErr = err
			if !alive(childPID) {
				return fmt.Errorf("detached daemon pid %d exited before owning the PID file", childPID)
			}
		}

		if timeout <= 0 || pollInterval <= 0 || waited >= timeout {
			if lastReadErr != nil {
				return fmt.Errorf("startup could not be confirmed within %s: read daemon PID file: %w", timeout, lastReadErr)
			}
			return fmt.Errorf("startup could not be confirmed within %s", timeout)
		}
		delay := min(pollInterval, timeout-waited)
		sleep(delay)
		waited += delay
	}
}

// confirmDetachedStartStability re-checks, in pollInterval steps for up to
// stabilityWindow (capped by remainingBudget), that childPID is still alive
// and still owns the PID file at path. It returns nil the instant the
// window elapses without either check failing; a non-positive window or
// poll interval is treated as "no further confirmation required" so the
// original immediate-success behavior is preserved.
func confirmDetachedStartStability(path string, childPID int, remainingBudget, pollInterval, stabilityWindow time.Duration, read func(string) (int, error), alive func(int) bool, sleep func(time.Duration)) error {
	window := min(stabilityWindow, remainingBudget)
	if window <= 0 || pollInterval <= 0 {
		return nil
	}
	for waited := time.Duration(0); waited < window; {
		delay := min(pollInterval, window-waited)
		sleep(delay)
		waited += delay
		if !alive(childPID) {
			return fmt.Errorf("detached daemon pid %d exited during startup shortly after owning the PID file", childPID)
		}
		ownerPID, err := read(path)
		if err != nil {
			return fmt.Errorf("detached daemon pid %d lost ownership of the PID file during startup: %w", childPID, err)
		}
		if ownerPID != childPID {
			return fmt.Errorf("detached daemon pid %d lost ownership of the PID file to pid %d during startup", childPID, ownerPID)
		}
	}
	return nil
}

func detachedLogPath() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for detached daemon log: %w", err)
		}
		return filepath.Join(home, "Library", "Logs", "termp.log"), nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache directory for detached daemon log: %w", err)
	}
	return filepath.Join(cacheDir, "termp", "termp.log"), nil
}
