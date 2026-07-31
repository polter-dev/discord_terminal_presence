//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processLooksLikeTermp(pid int) bool {
	return processLooksLikeTermpAtPath(pid, "")
}

func processLooksLikeTermpAtPath(pid int, expectedPath string) bool {
	return validateOtherProcess(pid, expectedPath) == nil
}

func currentProcessExecutablePath() (string, error) {
	current, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(current)
}

func processStartTime(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid PID")
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	cmd.Env = canonicalProcessTimeEnvironment(os.Environ())
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	startTime, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.TrimSpace(string(output)), time.UTC)
	if err != nil {
		return 0, err
	}
	return uint64(startTime.Unix()), nil
}

func canonicalProcessTimeEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		if strings.HasPrefix(entry, "LC_ALL=") || strings.HasPrefix(entry, "TZ=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "LC_ALL=C", "TZ=UTC")
}

func signalTermpProcessAtPath(pid int, expectedPath string) error {
	if err := validateOtherProcess(pid, expectedPath); err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func validateOtherProcess(pid int, expectedPath string) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}
	var err error
	if expectedPath == "" {
		expectedPath, err = currentProcessExecutablePath()
		if err != nil {
			return err
		}
	} else {
		expectedPath, err = filepath.EvalSymlinks(expectedPath)
		if err != nil {
			return err
		}
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "uid=", "-o", "comm=").Output()
	if err != nil {
		return err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return errors.New("cannot determine process identity")
	}
	uid, err := strconv.Atoi(fields[0])
	if err != nil || uid != os.Geteuid() {
		return errors.New("process owner does not match")
	}
	actual, err := filepath.EvalSymlinks(strings.Join(fields[1:], " "))
	if err != nil || filepath.Clean(actual) != filepath.Clean(expectedPath) {
		return fmt.Errorf("process executable does not match")
	}
	return nil
}
