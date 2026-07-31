//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func processLooksLikeTermp(pid int) bool {
	return processLooksLikeTermpAtPath(pid, "")
}

func processLooksLikeTermpAtPath(pid int, expectedPath string) bool {
	return validateLinuxProcess(pid, expectedPath) == nil
}

func currentProcessExecutablePath() (string, error) {
	currentPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine current executable: %w", err)
	}
	currentPath, err = normalizeLinuxExecutablePath(currentPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve current executable: %w", err)
	}
	return currentPath, nil
}

func processStartTime(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid PID")
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, fmt.Errorf("read process start time: %w", err)
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, errors.New("cannot parse process start time")
	}
	fields := strings.Fields(string(data[end+1:]))
	const startTimeIndexAfterCommand = 19
	if len(fields) <= startTimeIndexAfterCommand {
		return 0, errors.New("cannot parse process start time")
	}
	startTime, err := strconv.ParseUint(fields[startTimeIndexAfterCommand], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process start time: %w", err)
	}
	return startTime, nil
}

func signalTermpProcess(pid int) error {
	return signalTermpProcessAtPath(pid, "")
}

func signalTermpProcessAtPath(pid int, expectedPath string) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}

	pidfd, err := unix.PidfdOpen(pid, 0)
	if err == nil {
		defer unix.Close(pidfd)
		if err := validateLinuxProcess(pid, expectedPath); err != nil {
			return err
		}
		fdinfo, err := os.ReadFile(filepath.Join("/proc/self/fdinfo", strconv.Itoa(pidfd)))
		if err != nil {
			return fmt.Errorf("cannot bind pidfd identity: %w", err)
		}
		if !pidfdInfoMatchesPID(fdinfo, pid) {
			return errors.New("process identity changed during validation")
		}
		if err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0); err != nil {
			if pidfdUnavailable(err) {
				return signalLinuxByPID(pid, expectedPath)
			}
			return fmt.Errorf("pidfd signal failed: %w", err)
		}
		return nil
	}
	if !pidfdUnavailable(err) {
		return fmt.Errorf("pidfd_open failed: %w", err)
	}

	// Older kernels and restricted runtimes cannot create pidfds. Re-check the
	// full identity immediately before the PID-based signal.
	return signalLinuxByPID(pid, expectedPath)
}

func signalLinuxByPID(pid int, expectedPath string) error {
	if err := validateLinuxProcess(pid, expectedPath); err != nil {
		return err
	}
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		return fmt.Errorf("signal failed: %w", err)
	}
	return nil
}

func pidfdInfoMatchesPID(fdinfo []byte, pid int) bool {
	if pid <= 0 {
		return false
	}
	for _, line := range strings.Split(string(fdinfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "Pid:" {
			continue
		}
		got, err := strconv.Atoi(fields[1])
		return err == nil && got == pid
	}
	return false
}

func pidfdUnavailable(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}

func validateLinuxProcess(pid int, expectedPath string) error {
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
		expectedPath = filepath.Clean(expectedPath)
	}

	procPath := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := os.Stat(procPath)
	if err != nil {
		return fmt.Errorf("cannot inspect process: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine process owner")
	}
	targetPath, err := resolveLinuxProcessExecutablePath(filepath.Join(procPath, "exe"))
	if err != nil {
		return fmt.Errorf("cannot resolve process executable: %w", err)
	}
	if !linuxIdentityMatches(stat.Uid, uint32(os.Geteuid()), targetPath, expectedPath) {
		return errors.New("process executable or owner does not match recorded termp daemon")
	}
	return nil
}

func resolveLinuxProcessExecutablePath(procExePath string) (string, error) {
	resolvedPath, resolveErr := filepath.EvalSymlinks(procExePath)
	if resolveErr == nil {
		return resolvedPath, nil
	}
	targetPath, err := os.Readlink(procExePath)
	if err != nil {
		return "", resolveErr
	}
	const deletedSuffix = " (deleted)"
	if !strings.HasSuffix(targetPath, deletedSuffix) {
		return "", resolveErr
	}
	targetPath = strings.TrimSuffix(targetPath, deletedSuffix)
	if normalized, err := normalizeLinuxExecutablePath(targetPath); err == nil {
		return normalized, nil
	}
	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func normalizeLinuxExecutablePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func linuxIdentityMatches(actualUID, currentUID uint32, actualPath, currentPath string) bool {
	return actualUID == currentUID && actualPath != "" && currentPath != "" &&
		filepath.Clean(actualPath) == filepath.Clean(currentPath)
}
