//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func lockLogRotation(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockLogRotation(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

// openLogFile opens the daemon log for append, refusing to follow a symlink
// left (or planted) at path, then verifies the opened file is a regular file
// owned by the current user and tightens it to perm regardless of whatever
// mode it was created or left with (issue #561). O_NOFOLLOW makes the open
// itself fail closed on a symlink rather than silently writing through it.
func openLogFile(path string, perm os.FileMode) (*os.File, error) {
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY | syscall.O_NOFOLLOW
	file, err := os.OpenFile(path, flags, perm)
	if err != nil {
		return nil, err
	}
	if err := secureLogFileHandle(file, perm); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// tightenLogDirectoryPermissions chmods dir to 0700. os.MkdirAll no-ops on an
// already-existing directory and never tightens its mode, so a log directory
// created earlier under a looser umask stays loose until this runs
// (issue #561).
func tightenLogDirectoryPermissions(dir string) error {
	return os.Chmod(dir, 0o700)
}

func secureLogFileHandle(file *os.File, perm os.FileMode) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("daemon log %q is not a regular file", file.Name())
	}
	if err := requireCurrentUserOwner(info, "daemon log"); err != nil {
		return err
	}
	if info.Mode().Perm() != perm {
		if err := file.Chmod(perm); err != nil {
			return fmt.Errorf("tighten daemon log permissions: %w", err)
		}
	}
	return nil
}
