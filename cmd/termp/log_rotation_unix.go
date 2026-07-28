//go:build !windows

package main

import (
	"os"
	"syscall"
)

func lockLogRotation(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockLogRotation(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
