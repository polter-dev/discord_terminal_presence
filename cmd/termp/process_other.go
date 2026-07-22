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
	return validateOtherProcess(pid) == nil
}

func signalTermpProcess(pid int) error {
	if err := validateOtherProcess(pid); err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func validateOtherProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return err
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
	if err != nil || filepath.Clean(actual) != filepath.Clean(current) {
		return fmt.Errorf("process executable does not match")
	}
	return nil
}
