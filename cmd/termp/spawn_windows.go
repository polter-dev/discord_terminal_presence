//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	detachedProcess = 0x00000008
	createNoWindow  = 0x08000000
)

func detachedExecutable() (string, error) {
	imagePath, err := currentProcessExecutablePath()
	if err != nil {
		return "", err
	}
	return windowsTermpSibling(imagePath)
}

// windowsTermpSibling bypasses an invocation shim and returns the real console
// binary beside the running process image. Launching a Scoop shim here lets the
// shim allocate a visible console for its own child (#508, #510).
func windowsTermpSibling(imagePath string) (string, error) {
	return filepath.Abs(filepath.Join(filepath.Dir(imagePath), "termp.exe"))
}

func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess | createNoWindow,
	}
}

func startDetachedProcess(command *exec.Cmd) error {
	configureDetachedProcess(command)
	return command.Start()
}
