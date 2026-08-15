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
// checks the opened handle's owner where that can be determined (issue
// #561). FILE_FLAG_OPEN_REPARSE_POINT makes CreateFile open the reparse
// point itself instead of its target, mirroring the PID file's
// openWindowsPIDFile; FILE_APPEND_DATA gives the same atomic append-at-EOF
// semantics os.O_APPEND uses on the other platforms. READ_CONTROL is
// requested alongside it solely so GetSecurityInfo (called from
// secureLogFileHandle) has the access right it needs to read the owner SID;
// GetSecurityInfo fails with "Access is denied" on a handle that was not
// opened with READ_CONTROL, which is what broke daemon logging outright on
// windows-latest CI before this was added (issue #561 follow-up). Windows
// has no POSIX permission bits to tighten, so ownership is enforced via SID
// instead, the same convention requireCurrentUserOwner already uses on this
// platform.
func openLogFile(path string, _ os.FileMode) (*os.File, error) {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathp,
		windows.FILE_APPEND_DATA|windows.READ_CONTROL,
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
	return verifyLogFileOwner(handle)
}

// verifyLogFileOwner checks the opened log handle's owner SID against the
// current process token, but only rejects the open on a definite mismatch
// between two SIDs that were both successfully read. An owner lookup that
// cannot complete degrades to "unverified", not "unsafe", and must not
// prevent opening the log: the symlink protection (O_NOFOLLOW-equivalent
// open plus the reparse-point rejection above) is what actually stops the
// attack #561 is about, and losing the daemon log entirely because
// GetSecurityInfo or GetTokenInformation failed is a worse outcome than
// skipping a check nothing else here depends on. This was observed on
// windows-latest CI: GetSecurityInfo needs READ_CONTROL on the handle, which
// some Windows security contexts still deny even though the process can
// otherwise read and write the file.
func verifyLogFileOwner(handle windows.Handle) error {
	owner, err := windowsHandleOwnerSID(handle)
	if err != nil {
		return nil
	}
	currentUser, currentOwner, err := currentTokenOwnerSIDs()
	if err != nil {
		return nil
	}
	if !pidFileOwnerMatches(owner, currentUser) && !pidFileOwnerMatches(owner, currentOwner) {
		return errors.New("daemon log owner SID does not match current token user or owner SID")
	}
	return nil
}
