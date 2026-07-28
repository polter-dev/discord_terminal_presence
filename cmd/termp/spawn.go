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
	detachedChildFlag = "internal-detached-child"
	daemonLogFlag     = "internal-daemon-log"
)

const (
	detachedStartTimeout      = 2 * time.Second
	detachedStartPollInterval = 25 * time.Millisecond
)

func detachedChildArgs(enableVerbose bool) []string {
	args := []string{"start", "--" + detachedChildFlag}
	if enableVerbose {
		args = append(args, "--verbose")
	}
	return args
}

func spawnDetachedStart(enableVerbose bool) (int, string, error) {
	executable, err := os.Executable()
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
	if err := waitForDetachedStart(pidFilePath(), pid, detachedStartTimeout, detachedStartPollInterval, readPID, processAlive, processLooksLikeTermp, time.Sleep); err != nil {
		return 0, "", fmt.Errorf("%w; logs: %s", err, logPath)
	}
	return pid, logPath, nil
}

func waitForDetachedStart(path string, childPID int, timeout, pollInterval time.Duration, read func(string) (int, error), alive, looksLikeTermp func(int) bool, sleep func(time.Duration)) error {
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
				return nil
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
