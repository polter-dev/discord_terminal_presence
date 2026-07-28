//go:build windows

package service

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

func canonicalWindowsExecutable(value string) (string, error) {
	handle, err := openWindowsPath(value)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buffer := make([]uint16, 512)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", fmt.Errorf("resolve final executable path: %w", err)
		}
		if length < uint32(len(buffer)) {
			finalPath := windows.UTF16ToString(buffer[:length])
			finalPath = strings.TrimPrefix(finalPath, `\\?\`)
			if strings.HasPrefix(finalPath, `UNC\`) {
				finalPath = `\\` + strings.TrimPrefix(finalPath, `UNC\`)
			}
			return normalizeResolvedWindowsPath(finalPath), nil
		}
		buffer = make([]uint16, length+1)
	}
}

func sameWindowsFileIdentity(first, second string) (same, determined bool) {
	firstHandle, err := openWindowsPath(first)
	if err != nil {
		return false, false
	}
	defer windows.CloseHandle(firstHandle)
	secondHandle, err := openWindowsPath(second)
	if err != nil {
		return false, false
	}
	defer windows.CloseHandle(secondHandle)

	var firstInfo, secondInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(firstHandle, &firstInfo); err != nil {
		return false, false
	}
	if err := windows.GetFileInformationByHandle(secondHandle, &secondInfo); err != nil {
		return false, false
	}
	return firstInfo.VolumeSerialNumber == secondInfo.VolumeSerialNumber &&
		firstInfo.FileIndexHigh == secondInfo.FileIndexHigh &&
		firstInfo.FileIndexLow == secondInfo.FileIndexLow, true
}

func openWindowsPath(value string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(normalizeResolvedWindowsPath(value))
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}
