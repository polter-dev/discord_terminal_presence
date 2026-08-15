//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func redirectStderr(file *os.File) error {
	oldFile := os.Stderr
	oldHandle, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(file.Fd())); err != nil {
		return err
	}
	os.Stderr = file
	if oldHandle != 0 && oldHandle != windows.InvalidHandle && oldHandle != windows.Handle(file.Fd()) {
		_ = oldFile.Close()
	}
	return nil
}
