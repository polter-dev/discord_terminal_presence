//go:build windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == windowsStillActive
}

func processLooksLikeTermp(pid int) bool {
	return processLooksLikeTermpAtPath(pid, "")
}

func processLooksLikeTermpAtPath(pid int, expectedPath string) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	return validateWindowsProcessHandle(handle, expectedPath) == nil
}

func currentProcessExecutablePath() (string, error) {
	path, err := processImagePath(windows.CurrentProcess())
	if err != nil {
		return "", fmt.Errorf("cannot determine current image path: %w", err)
	}
	return normalizeWindowsImagePath(path), nil
}

func processStartTime(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid PID")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("open process for start time: %w", err)
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, fmt.Errorf("read process start time: %w", err)
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

func signalTermpProcessAtPath(pid int, expectedPath string) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}
	handle, err := windowsOpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("cannot open process: %w", err)
	}
	defer windowsCloseHandle(handle)
	if err := validateWindowsProcessHandleForSignal(handle, expectedPath); err != nil {
		return err
	}

	name, nameErr := windows.UTF16PtrFromString(shutdownEventName(pid))
	if nameErr == nil {
		event, err := windowsOpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if err == nil {
			defer windowsCloseHandle(event)
			if err := windowsSetEvent(event); err == nil {
				return nil
			}
		}
	} else {
		debugf("shutdown event name invalid: %v", nameErr)
	}

	// Validation and termination use the same kernel handle, so PID recycling
	// cannot redirect termination to another process.
	if err := windowsTerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate process: %w", err)
	}
	return nil
}

var (
	windowsOpenEvent                      = windows.OpenEvent
	windowsSetEvent                       = windows.SetEvent
	windowsOpenProcess                    = windows.OpenProcess
	windowsCloseHandle                    = windows.CloseHandle
	windowsTerminateProcess               = windows.TerminateProcess
	validateWindowsProcessHandleForSignal = validateWindowsProcessHandle
)

func validateWindowsProcessHandle(handle windows.Handle, expectedPath string) error {
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return fmt.Errorf("cannot determine process state: %w", err)
	}
	if exitCode != windowsStillActive {
		return errors.New("process is no longer running")
	}
	actualSID, err := processUserSID(handle)
	if err != nil {
		return fmt.Errorf("cannot determine process user SID: %w", err)
	}
	currentSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("cannot determine current user SID: %w", err)
	}
	actualPath, err := processImagePath(handle)
	if err != nil {
		return fmt.Errorf("cannot determine process image path: %w", err)
	}
	if expectedPath == "" {
		expectedPath, err = currentProcessExecutablePath()
		if err != nil {
			return err
		}
	} else {
		expectedPath = normalizeWindowsImagePath(expectedPath)
	}
	if !windowsIdentityMatches(actualSID, currentSID, actualPath, expectedPath) {
		return errors.New("process executable or owner SID does not match recorded termp daemon")
	}
	return nil
}

func processUserSID(handle windows.Handle) (*windows.SID, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func processImagePath(handle windows.Handle) (string, error) {
	return processImagePathWithQuery(func(buffer *uint16, length *uint32) error {
		return windows.QueryFullProcessImageName(handle, 0, buffer, length)
	})
}

func processImagePathWithQuery(query func(*uint16, *uint32) error) (string, error) {
	const maxSize = uint32(32768)
	for size := uint32(260); ; size = min(size*2, maxSize) {
		buffer := make([]uint16, size)
		length := size
		err := query(&buffer[0], &length)
		if err == nil {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			return "", err
		}
		if size == maxSize {
			break
		}
	}
	return "", windows.ERROR_INSUFFICIENT_BUFFER
}

func windowsIdentityMatches(actualSID, currentSID *windows.SID, actualPath, currentPath string) bool {
	return sameSID(actualSID, currentSID) && actualPath != "" && currentPath != "" &&
		strings.EqualFold(normalizeWindowsImagePath(actualPath), normalizeWindowsImagePath(currentPath))
}

func normalizeWindowsImagePath(path string) string {
	path = strings.TrimPrefix(path, `\\?\`)
	return filepath.Clean(path)
}
