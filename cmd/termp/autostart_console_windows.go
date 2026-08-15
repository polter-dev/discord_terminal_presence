//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	autostartKernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleTitle     = autostartKernel32.NewProc("SetConsoleTitleW")
	procReleaseTermpConsole = autostartKernel32.NewProc("FreeConsole")
)

func setAutostartConsoleTitle(value string) error {
	title, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return fmt.Errorf("encode console title: %w", err)
	}
	if result, _, callErr := procSetConsoleTitle.Call(uintptr(unsafe.Pointer(title))); result == 0 {
		return callErr
	}
	return nil
}

func freeAutostartConsole() error {
	if result, _, callErr := procReleaseTermpConsole.Call(); result == 0 {
		return callErr
	}
	return nil
}
