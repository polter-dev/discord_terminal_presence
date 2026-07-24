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
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	return validateWindowsProcessHandle(handle) == nil
}

func signalTermpProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}
	name, nameErr := windows.UTF16PtrFromString(shutdownEventName(pid))
	if nameErr == nil {
		event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if err == nil {
			defer windows.CloseHandle(event)
			if err := windows.SetEvent(event); err != nil {
				return fmt.Errorf("signal shutdown event: %w", err)
			}
			return nil
		}
	} else {
		debugf("shutdown event name invalid: %v", nameErr)
	}

	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("cannot open process: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := validateWindowsProcessHandle(handle); err != nil {
		return err
	}
	// Validation and termination use the same kernel handle, so PID recycling
	// cannot redirect termination to another process.
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate process: %w", err)
	}
	return nil
}

func validateWindowsProcessHandle(handle windows.Handle) error {
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
	currentPath, err := processImagePath(windows.CurrentProcess())
	if err != nil {
		return fmt.Errorf("cannot determine current image path: %w", err)
	}
	if !windowsIdentityMatches(actualSID, currentSID, actualPath, currentPath) {
		return errors.New("process executable or owner SID does not match current termp")
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
