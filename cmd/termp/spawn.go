package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const detachedChildFlag = "internal-detached-child"

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
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("open detached daemon log: %w", err)
	}
	defer logFile.Close()
	nullFile, err := os.Open(os.DevNull)
	if err != nil {
		return 0, "", fmt.Errorf("open null input: %w", err)
	}
	defer nullFile.Close()

	command := exec.Command(executable, detachedChildArgs(enableVerbose)...)
	command.Stdin = nullFile
	command.Stdout = logFile
	command.Stderr = logFile
	if err := startDetachedProcess(command); err != nil {
		return 0, "", fmt.Errorf("start detached daemon: %w", err)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	return pid, logPath, nil
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
