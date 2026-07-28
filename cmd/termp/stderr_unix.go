//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func redirectStderr(file *os.File) error {
	return unix.Dup2(int(file.Fd()), int(os.Stderr.Fd()))
}
