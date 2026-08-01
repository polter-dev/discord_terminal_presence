//go:build windows

package detector

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	procGetAncestor      = user32.NewProc("GetAncestor")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")

	consoleAttachMu sync.Mutex
)

const consoleProbePIDEnv = "TERMP_INTERNAL_CONSOLE_PROBE_PID"

func init() {
	value, ok := os.LookupEnv(consoleProbePIDEnv)
	_ = os.Unsetenv(consoleProbePIDEnv)
	if !consoleProbeRequested(os.Args, ok) {
		return
	}
	pid, err := strconv.ParseUint(value, 10, 32)
	if err != nil || pid == 0 {
		fmt.Fprintln(os.Stderr, "invalid console probe pid")
		os.Exit(2)
	}
	hwnd, conPTY, err := inspectWindowsConsole(uint32(pid))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stdout, "%d %t\n", hwnd, conPTY)
	os.Exit(0)
}

func newSystemTTYResolver() TTYResolver {
	return newWindowsTTYResolver(realWindowsConsoleHWNDForPID)
}

func newSystemTTYAtimeSource() TTYAtimeSource {
	return &windowsTTYAtimeSource{
		foregroundWindow: func() uintptr {
			return uintptr(windows.GetForegroundWindow())
		},
		rootOwnerWindow: func(hwnd uintptr) uintptr {
			const gaRootOwner = 3
			rootOwner, _, _ := procGetAncestor.Call(hwnd, gaRootOwner)
			return rootOwner
		},
		windowExists: func(hwnd uintptr) bool {
			return windows.IsWindow(windows.HWND(hwnd))
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
	executable, err := os.Executable()
	if err != nil {
		consoleAttachMu.Unlock()
		return 0, false, fmt.Errorf("resolve console probe executable: %w", err)
	}
	cmd := exec.Command(executable, consoleProbeArg)
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, consoleProbePIDEnv+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, consoleProbePIDEnv+"="+strconv.FormatInt(int64(pid), 10))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	var output, stderr bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		consoleAttachMu.Unlock()
		return 0, false, fmt.Errorf("start console probe: %w", err)
	}
	consoleAttachMu.Unlock()

	if err := cmd.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return 0, false, fmt.Errorf("console probe: %s", message)
		}
		return 0, false, fmt.Errorf("console probe: %w", err)
	}
	if _, err := fmt.Fscan(&output, &hwnd, &conPTY); err != nil {
		return 0, false, fmt.Errorf("parse console probe result: %w", err)
	}
	return hwnd, conPTY, nil
}

func inspectWindowsConsole(pid uint32) (hwnd uintptr, conPTY bool, retErr error) {
	// AttachConsole and FreeConsole reset a process's console control-handler
	// table. Probe in this short-lived child so the daemon's Go runtime handler,
	// and therefore signal.Notify Ctrl+C/close delivery, is never disturbed.
	_ = freeConsole()
	defer freeConsole()

	if err := attachConsole(pid); err != nil {
		return 0, false, fmt.Errorf("attach candidate console: %w", err)
	}
	hwnd = getConsoleWindow()
	// A headless ConPTY conhost can return a non-null hidden window handle on
	// some Windows builds; resolving it would freeze the activity clock and let
	// idle-clear drop an active session (#501). Fail open for the zero handle
	// and for any handle that is not a real, visible terminal window.
	if shouldConsoleFailOpen(windowsConsoleWindow{
		hwnd:    hwnd,
		exists:  hwnd != 0 && windows.IsWindow(windows.HWND(hwnd)),
		visible: hwnd != 0 && isWindowVisible(hwnd),
	}) {
		return 0, true, nil
	}
	return hwnd, false, nil
}

func isWindowVisible(hwnd uintptr) bool {
	r1, _, _ := procIsWindowVisible.Call(hwnd)
	return r1 != 0
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
