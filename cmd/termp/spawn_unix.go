//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func startDetachedProcess(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command.Start()
}
