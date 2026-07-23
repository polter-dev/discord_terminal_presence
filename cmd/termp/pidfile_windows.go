//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPIDFile(path string) (*os.File, error) {
	return openWindowsPIDFile(path, windows.GENERIC_WRITE, windows.CREATE_NEW, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
}

func publishPIDFile(pendingPath, path string) error {
	err := windows.MoveFile(windows.StringToUTF16Ptr(pendingPath), windows.StringToUTF16Ptr(path))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
		return os.ErrExist
	}
	return err
}

func openPIDFile(path string) (*os.File, error) {
	return openWindowsPIDFile(path, windows.GENERIC_READ, windows.OPEN_EXISTING, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
}

func lockPIDFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
}

func openWindowsPIDFile(path string, access, disposition, shareMode uint32) (*os.File, error) {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathp,
		access,
		shareMode,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

type fileAttributeTagInfo struct {
	attributes uint32
	reparseTag uint32
}

func validatePIDFileHandle(file *os.File, path string) error {
	handle := windows.Handle(file.Fd())
	var attributes fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attributes)),
		uint32(unsafe.Sizeof(attributes)),
	); err != nil {
		return fmt.Errorf("cannot determine PID file attributes: %w", err)
	}
	if !pidFileAttributesSafe(attributes.attributes) {
		return fmt.Errorf("PID file %q is a reparse point", path)
	}
	owner, err := windowsHandleOwnerSID(handle)
	if err != nil {
		return fmt.Errorf("cannot determine PID file owner: %w", err)
	}
	currentUser, currentOwner, err := currentTokenOwnerSIDs()
	if err != nil {
		return fmt.Errorf("cannot determine current token owner SIDs: %w", err)
	}
	if !pidFileOwnerMatches(owner, currentUser) && !pidFileOwnerMatches(owner, currentOwner) {
		return errors.New("PID file owner SID does not match current token user or owner SID")
	}
	return nil
}

func pidFileAttributesSafe(attributes uint32) bool {
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

func pidFileOwnerMatches(owner, current *windows.SID) bool {
	return sameSID(owner, current)
}

func windowsHandleOwnerSID(handle windows.Handle) (*windows.SID, error) {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := descriptor.Owner()
	return owner, err
}

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

type tokenOwner struct {
	owner *windows.SID
}

func currentTokenOwnerSIDs() (*windows.SID, *windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, nil, err
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	userSID, err := user.User.Sid.Copy()
	if err != nil {
		return nil, nil, err
	}

	var size uint32
	err = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, nil, err
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size); err != nil {
		return nil, nil, err
	}
	owner := (*tokenOwner)(unsafe.Pointer(&buffer[0])).owner
	if owner == nil {
		return nil, nil, errors.New("current token has no owner SID")
	}
	ownerSID, err := owner.Copy()
	if err != nil {
		return nil, nil, err
	}
	return userSID, ownerSID, nil
}

func sameSID(left, right *windows.SID) bool {
	return left != nil && right != nil && left.Equals(right)
}

func requireCurrentUserOwner(_ os.FileInfo, _ string) error {
	// PID-file SID ownership is validated against the opened handle. Directory
	// ownership has no os.FileInfo SID representation on Windows.
	return nil
}
