//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func createPIDFile(path string) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC | os.O_EXCL | syscall.O_NOFOLLOW
	return os.OpenFile(path, flags, 0o600)
}

func publishPIDFile(pendingPath, path string) error {
	if err := os.Link(pendingPath, path); err != nil {
		return err
	}
	// The authoritative name is now published. Failure to remove the private
	// alias must not make the caller mistake successful publication for failure.
	_ = os.Remove(pendingPath)
	return nil
}

func openPIDFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

func lockPIDFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func validatePIDFileHandle(_ *os.File, _ string) error {
	return nil
}

func requireCurrentUserOwner(info os.FileInfo, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine %s owner", label)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s is owned by uid %d, not current uid %d", label, stat.Uid, os.Geteuid())
	}
	return nil
}
