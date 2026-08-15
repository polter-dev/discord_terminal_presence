//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func lockLogRotation(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func unlockLogRotation(file *os.File) {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}

// openLogFile opens the daemon log for append, refusing to transparently
// follow a reparse point (symlink/junction) left or planted at path, then
// verifies the opened handle is a plain file owned by the current user
// (issue #561). FILE_FLAG_OPEN_REPARSE_POINT makes CreateFile open the
// reparse point itself instead of its target, mirroring the PID file's
// openWindowsPIDFile; FILE_APPEND_DATA gives the same atomic append-at-EOF
// semantics os.O_APPEND uses on the other platforms. Windows has no POSIX
// permission bits to tighten, so ownership is enforced via SID instead, the
// same convention requireCurrentUserOwner already uses on this platform.
func openLogFile(path string, _ os.FileMode) (*os.File, error) {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathp,
		windows.FILE_APPEND_DATA,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open daemon log: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if err := secureLogFileHandle(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// tightenLogDirectoryPermissions is a no-op on Windows: there are no POSIX
// permission bits to tighten, and directory ACLs are left to the platform
// default the same way requireCurrentUserOwner treats directory ownership
// (see pidfile_windows.go).
func tightenLogDirectoryPermissions(string) error {
	return nil
}

func secureLogFileHandle(file *os.File) error {
	handle := windows.Handle(file.Fd())
	var attributes fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attributes)),
		uint32(unsafe.Sizeof(attributes)),
	); err != nil {
		return fmt.Errorf("cannot determine daemon log attributes: %w", err)
	}
	if !pidFileAttributesSafe(attributes.attributes) {
		return fmt.Errorf("daemon log %q is a reparse point", file.Name())
	}
	owner, err := windowsHandleOwnerSID(handle)
	if err != nil {
		return fmt.Errorf("cannot determine daemon log owner: %w", err)
	}
	currentUser, currentOwner, err := currentTokenOwnerSIDs()
	if err != nil {
		return fmt.Errorf("cannot determine current token owner SIDs: %w", err)
	}
	if !pidFileOwnerMatches(owner, currentUser) && !pidFileOwnerMatches(owner, currentOwner) {
		return errors.New("daemon log owner SID does not match current token user or owner SID")
	}
	return nil
}
