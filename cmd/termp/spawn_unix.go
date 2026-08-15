//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func detachedExecutable() (string, error) {
	return os.Executable()
}

func startDetachedProcess(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command.Start()
}
