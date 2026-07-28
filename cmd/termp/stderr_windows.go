//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func redirectStderr(file *os.File) error {
	old, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(file.Fd())); err != nil {
		return err
	}
	os.Stderr = file
	if old != 0 && old != windows.InvalidHandle && old != windows.Handle(file.Fd()) {
		_ = windows.CloseHandle(old)
	}
	return nil
}
