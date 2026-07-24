//go:build windows

package main

import (
	"context"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

func shutdownEventName(pid int) string {
	return `Local\TermpDaemonShutdown-` + strconv.Itoa(pid)
}

func installShutdownSignal(cancel context.CancelFunc) func() {
	name, err := windows.UTF16PtrFromString(shutdownEventName(os.Getpid()))
	if err != nil {
		debugf("shutdown event name invalid: %v", err)
		return func() {}
	}
	shutdownEvent, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil {
		debugf("shutdown event disabled: %v", err)
		return func() {}
	}
	stopEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		debugf("shutdown event cleanup disabled: %v", err)
		_ = windows.CloseHandle(shutdownEvent)
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runShutdownSignalWait(func() (shutdownWaitResult, error) {
			event, err := windows.WaitForMultipleObjects([]windows.Handle{shutdownEvent, stopEvent}, false, windows.INFINITE)
			if err != nil {
				return shutdownWaitStop, err
			}
			if event == windows.WAIT_OBJECT_0 {
				return shutdownWaitCancel, nil
			}
			return shutdownWaitStop, nil
		}, cancel)
	}()

	return func() {
		_ = windows.SetEvent(stopEvent)
		<-done
		_ = windows.CloseHandle(stopEvent)
		_ = windows.CloseHandle(shutdownEvent)
	}
}
