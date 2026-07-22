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
	return validateLinuxProcess(pid) == nil
}

func signalTermpProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}

	pidfd, err := unix.PidfdOpen(pid, 0)
	if err == nil {
		defer unix.Close(pidfd)
		if err := validateLinuxProcess(pid); err != nil {
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
				return signalLinuxByPID(pid)
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
	return signalLinuxByPID(pid)
}

func signalLinuxByPID(pid int) error {
	if err := validateLinuxProcess(pid); err != nil {
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

func validateLinuxProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine current executable: %w", err)
	}
	currentPath, err = normalizeLinuxExecutablePath(currentPath)
	if err != nil {
		return fmt.Errorf("cannot resolve current executable: %w", err)
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
	targetPath, err := filepath.EvalSymlinks(filepath.Join(procPath, "exe"))
	if err != nil {
		return fmt.Errorf("cannot resolve process executable: %w", err)
	}
	if !linuxIdentityMatches(stat.Uid, uint32(os.Geteuid()), targetPath, currentPath) {
		return errors.New("process executable or owner does not match current termp")
	}
	return nil
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
