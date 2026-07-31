//go:build darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type darwinProcessIdentity struct {
	uid       uint32
	path      string
	startSec  int64
	startUsec int32
}

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
	_, err := validatedDarwinIdentity(pid, expectedPath)
	return err == nil
}

func currentProcessExecutablePath() (string, error) {
	currentPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine current executable: %w", err)
	}
	currentPath, err = normalizeDarwinExecutablePath(currentPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve current executable: %w", err)
	}
	return currentPath, nil
}

func processStartTime(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid PID")
	}
	kinfo, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("read process start time: %w", err)
	}
	if kinfo.Proc.P_starttime.Sec < 0 || kinfo.Proc.P_starttime.Usec < 0 {
		return 0, errors.New("invalid process start time")
	}
	return uint64(kinfo.Proc.P_starttime.Sec)*1_000_000 + uint64(kinfo.Proc.P_starttime.Usec), nil
}

func signalTermpProcessAtPath(pid int, expectedPath string) error {
	first, err := validatedDarwinIdentity(pid, expectedPath)
	if err != nil {
		return err
	}
	second, err := validatedDarwinIdentity(pid, expectedPath)
	if err != nil {
		return err
	}
	if !sameDarwinProcess(first, second) {
		return errors.New("process identity changed before signaling")
	}
	// Darwin has no pidfd equivalent. The stable start time, owner, and full
	// image path are re-read immediately before this PID-based signal.
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		return fmt.Errorf("signal failed: %w", err)
	}
	return nil
}

func validatedDarwinIdentity(pid int, expectedPath string) (darwinProcessIdentity, error) {
	if pid <= 0 {
		return darwinProcessIdentity{}, errors.New("invalid PID")
	}
	kinfo, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return darwinProcessIdentity{}, fmt.Errorf("cannot inspect process: %w", err)
	}
	if expectedPath == "" {
		expectedPath, err = currentProcessExecutablePath()
		if err != nil {
			return darwinProcessIdentity{}, err
		}
	} else {
		expectedPath = filepath.Clean(expectedPath)
	}
	rawImagePath, err := darwinProcessImage(pid)
	if err != nil {
		return darwinProcessIdentity{}, err
	}
	imagePath, err := normalizeDarwinExecutablePath(rawImagePath)
	if err != nil {
		// A package upgrade can unlink the old image while the daemon is still
		// running. The PID record already contains the canonical path captured
		// at startup, and proc_pidpath returns the kernel's image path, so retain
		// that absolute identity when there is no longer a file to resolve.
		imagePath, err = filepath.Abs(rawImagePath)
		if err != nil {
			return darwinProcessIdentity{}, fmt.Errorf("cannot resolve process executable: %w", err)
		}
		imagePath = filepath.Clean(imagePath)
	}
	identity := darwinProcessIdentity{
		uid:       kinfo.Eproc.Ucred.Uid,
		path:      imagePath,
		startSec:  kinfo.Proc.P_starttime.Sec,
		startUsec: kinfo.Proc.P_starttime.Usec,
	}
	if !darwinIdentityMatches(identity.uid, uint32(os.Geteuid()), identity.path, expectedPath) {
		return darwinProcessIdentity{}, errors.New("process executable or owner does not match recorded termp daemon")
	}
	return identity, nil
}

func darwinProcessImage(pid int) (string, error) {
	if path, err := darwinKernelProcessImage(pid); err == nil && path != "" {
		return path, nil
	}
	procArgs, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", fmt.Errorf("cannot determine process image: %w", err)
	}
	path, ok := darwinExecutableFromProcArgs(procArgs)
	if !ok {
		return "", errors.New("cannot parse process image")
	}
	return path, nil
}

func normalizeDarwinExecutablePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func darwinKernelProcessImage(pid int) (string, error) {
	const (
		procInfoCallPIDInfo = 2
		procPIDPathInfo     = 11
		procPIDPathMaxSize  = 4 * 1024
	)
	buffer := make([]byte, procPIDPathMaxSize)
	length, _, errno := syscall.Syscall6(
		//lint:ignore SA1019 x/sys has no libSystem wrapper for proc_pidpath.
		unix.SYS_PROC_INFO,
		procInfoCallPIDInfo,
		uintptr(pid),
		procPIDPathInfo,
		0,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if errno != 0 {
		return "", errno
	}
	if length == 0 || length > uintptr(len(buffer)) {
		return "", errors.New("kernel returned an invalid process image")
	}
	if end := bytes.IndexByte(buffer[:length], 0); end >= 0 {
		length = uintptr(end)
	}
	if length == 0 {
		return "", errors.New("kernel returned an empty process image")
	}
	return string(buffer[:length]), nil
}

func darwinExecutableFromProcArgs(procArgs []byte) (string, bool) {
	const argcSize = 4
	if len(procArgs) <= argcSize {
		return "", false
	}
	end := bytes.IndexByte(procArgs[argcSize:], 0)
	if end <= 0 {
		return "", false
	}
	return string(procArgs[argcSize : argcSize+end]), true
}

func darwinIdentityMatches(kinfoUID, currentUID uint32, actualPath, currentPath string) bool {
	return kinfoUID == currentUID && actualPath != "" && currentPath != "" &&
		filepath.Clean(actualPath) == filepath.Clean(currentPath)
}

func sameDarwinProcess(left, right darwinProcessIdentity) bool {
	return left.uid == right.uid && left.path == right.path &&
		left.startSec == right.startSec && left.startUsec == right.startUsec
}
