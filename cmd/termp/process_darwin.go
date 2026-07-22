//go:build darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	_, err := validatedDarwinIdentity(pid)
	return err == nil
}

func signalTermpProcess(pid int) error {
	first, err := validatedDarwinIdentity(pid)
	if err != nil {
		return err
	}
	second, err := validatedDarwinIdentity(pid)
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

func validatedDarwinIdentity(pid int) (darwinProcessIdentity, error) {
	if pid <= 0 {
		return darwinProcessIdentity{}, errors.New("invalid PID")
	}
	kinfo, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return darwinProcessIdentity{}, fmt.Errorf("cannot inspect process: %w", err)
	}
	currentPath, err := os.Executable()
	if err != nil {
		return darwinProcessIdentity{}, fmt.Errorf("cannot determine current executable: %w", err)
	}
	currentPath, err = filepath.EvalSymlinks(currentPath)
	if err != nil {
		return darwinProcessIdentity{}, fmt.Errorf("cannot resolve current executable: %w", err)
	}
	imagePath, err := darwinProcessImage(pid)
	if err != nil {
		return darwinProcessIdentity{}, err
	}
	imagePath, err = filepath.EvalSymlinks(imagePath)
	if err != nil {
		return darwinProcessIdentity{}, fmt.Errorf("cannot resolve process executable: %w", err)
	}
	identity := darwinProcessIdentity{
		uid:       kinfo.Eproc.Ucred.Uid,
		path:      imagePath,
		startSec:  kinfo.Proc.P_starttime.Sec,
		startUsec: kinfo.Proc.P_starttime.Usec,
	}
	if !darwinIdentityMatches(identity.uid, uint32(os.Geteuid()), identity.path, currentPath) {
		return darwinProcessIdentity{}, errors.New("process executable or owner does not match current termp")
	}
	return identity, nil
}

func darwinProcessImage(pid int) (string, error) {
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
