//go:build windows

package detector

import (
	"errors"
	"sync"

	"golang.org/x/sys/windows"
)

// windowsOwnerResolver compares a candidate process's token-owner SID
// against the daemon's own token-owner SID. gopsutil does not implement
// Uids() on Windows, so this resolves ownership directly via the Win32
// token APIs, mirroring the equivalent-purpose UID comparison on Unix.
type windowsOwnerResolver struct {
	once       sync.Once
	currentSID string
	currentErr error
}

func newSystemOwnerResolver() OwnerResolver {
	return &windowsOwnerResolver{}
}

// Owned reports whether pid's process token owner SID matches the daemon's
// own token owner SID. Any lookup failure (access denied, pid exited
// mid-scan, token query failure) is returned as an error so the caller
// fails closed rather than assuming ownership.
func (r *windowsOwnerResolver) Owned(pid int32) (bool, error) {
	r.once.Do(func() {
		r.currentSID, r.currentErr = tokenOwnerSID(windows.GetCurrentProcessToken())
	})
	if r.currentErr != nil {
		return false, r.currentErr
	}
	sid, err := processOwnerSID(uint32(pid))
	if err != nil {
		return false, err
	}
	return sid == r.currentSID, nil
}

func processOwnerSID(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer token.Close()

	return tokenOwnerSID(token)
}

func tokenOwnerSID(token windows.Token) (string, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	if user.User.Sid == nil {
		return "", errors.New("token has no user sid")
	}
	return user.User.Sid.String(), nil
}
