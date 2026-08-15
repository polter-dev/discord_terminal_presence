//go:build windows

package detector

import (
	"errors"
	"sync"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
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
// fails closed rather than assuming ownership. When createTime is non-zero,
// it must match pid's current creation time or Owned fails closed: a
// mismatch means pid was recycled since identity capture and no longer
// names the same process (#569).
func (r *windowsOwnerResolver) Owned(pid int32, createTime time.Time) (bool, error) {
	r.once.Do(func() {
		r.currentSID, r.currentErr = tokenOwnerSID(windows.GetCurrentProcessToken())
	})
	if r.currentErr != nil {
		return false, r.currentErr
	}
	if !createTime.IsZero() {
		proc, err := psprocess.NewProcess(pid)
		if err != nil {
			return false, err
		}
		millis, err := proc.CreateTime()
		if err != nil {
			return false, err
		}
		if millis <= 0 || !time.UnixMilli(millis).Equal(createTime) {
			return false, errors.New("process identity changed since it was first observed (pid reused)")
		}
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
