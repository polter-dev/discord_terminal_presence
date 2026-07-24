//go:build windows

package detector

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")

	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procFreeConsole      = kernel32.NewProc("FreeConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")

	consoleAttachMu sync.Mutex
)

func newSystemTTYResolver() TTYResolver {
	return windowsTTYResolver{consoleHWNDForPID: realWindowsConsoleHWNDForPID}
}

func newSystemTTYAtimeSource() TTYAtimeSource {
	return windowsTTYAtimeSource{
		foregroundWindow: func() uintptr {
			return uintptr(windows.GetForegroundWindow())
		},
		lastInputMillis: realWindowsLastInputMillis,
	}
}

func realWindowsConsoleHWNDForPID(pid int32) (hwnd uintptr, conPTY bool, retErr error) {
	if pid <= 0 {
		return 0, false, fmt.Errorf("invalid pid %d", pid)
	}
	if uint32(pid) == windows.GetCurrentProcessId() {
		return 0, false, errors.New("refusing to attach to own console")
	}

	consoleAttachMu.Lock()
	defer consoleAttachMu.Unlock()

	ownPID := windows.GetCurrentProcessId()
	originalHWND := getConsoleWindow()
	hadConsole := originalHWND != 0
	if hadConsole {
		if err := freeConsole(); err != nil {
			return 0, false, fmt.Errorf("detach current console: %w", err)
		}
	} else {
		_ = freeConsole()
	}

	defer func() {
		_ = freeConsole()
		if hadConsole {
			if err := attachConsole(ownPID); err != nil && retErr == nil {
				retErr = fmt.Errorf("restore original console: %w", err)
			}
		}
	}()

	if err := attachConsole(uint32(pid)); err != nil {
		return 0, false, fmt.Errorf("attach candidate console: %w", err)
	}
	hwnd = getConsoleWindow()
	if hwnd == 0 {
		return 0, true, nil
	}
	return hwnd, false, nil
}

func attachConsole(pid uint32) error {
	r1, _, err := procAttachConsole.Call(uintptr(pid))
	if r1 == 0 {
		return err
	}
	return nil
}

func freeConsole() error {
	r1, _, err := procFreeConsole.Call()
	if r1 == 0 {
		return err
	}
	return nil
}

func getConsoleWindow() uintptr {
	r1, _, _ := procGetConsoleWindow.Call()
	return r1
}

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

func realWindowsLastInputMillis() (uint32, bool) {
	info := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r1, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return 0, false
	}
	tick, _, _ := procGetTickCount.Call()
	return uint32(tick) - info.dwTime, true
}
