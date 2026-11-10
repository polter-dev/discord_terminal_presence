//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
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

// retiredConsoleStdFiles keeps the std files FreeConsole invalidated reachable.
// Their handle numbers are free for reuse (the log lock file often gets the old
// stderr number), so neither a Close nor their finalizer may ever run (#663).
var retiredConsoleStdFiles []*os.File

// retireConsoleStdFiles points os.Stdin, os.Stdout and os.Stderr at the null
// device after FreeConsole, so no later write or close, including
// redirectStderr's close of the old stderr, reaches a reused handle number.
func retireConsoleStdFiles() error {
	var errs []error
	for _, std := range []**os.File{&os.Stdin, &os.Stdout, &os.Stderr} {
		retiredConsoleStdFiles = append(retiredConsoleStdFiles, *std)
		var err error
		// On error this is a nil *os.File, whose methods fail harmlessly.
		if *std, err = os.OpenFile(os.DevNull, os.O_RDWR, 0); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
