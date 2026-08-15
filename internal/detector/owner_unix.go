//go:build unix

package detector

import (
	"errors"
	"os"
	"strconv"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
)

// unixOwnerResolver compares a candidate process's effective UID (via
// gopsutil, which reads /proc on Linux and the kernel process table on
// Darwin) against the daemon's own effective UID.
type unixOwnerResolver struct {
	currentUID string
}

func newSystemOwnerResolver() OwnerResolver {
	return &unixOwnerResolver{currentUID: strconv.Itoa(os.Geteuid())}
}

// Owned reports whether pid's effective UID matches the daemon's effective
// UID. Any lookup failure (permission denied, pid exited mid-scan) is
// returned as an error so the caller fails closed rather than assuming
// ownership. When createTime is non-zero, it must match pid's current
// creation time or Owned fails closed: a mismatch means pid was recycled
// since identity capture and no longer names the same process (#569).
func (r *unixOwnerResolver) Owned(pid int32, createTime time.Time) (bool, error) {
	proc, err := psprocess.NewProcess(pid)
	if err != nil {
		return false, err
	}
	if !createTime.IsZero() {
		millis, err := proc.CreateTime()
		if err != nil {
			return false, err
		}
		if millis <= 0 || !time.UnixMilli(millis).Equal(createTime) {
			return false, errors.New("process identity changed since it was first observed (pid reused)")
		}
	}
	uids, err := proc.Uids()
	if err != nil {
		return false, err
	}
	if len(uids) == 0 {
		return false, errors.New("no uid reported for process")
	}
	// gopsutil's Uids() layout differs by OS: Darwin returns a single
	// effective UID; Linux returns [real, effective, saved, filesystem] as
	// parsed from /proc/<pid>/status. Prefer index 1 (effective) when
	// present, falling back to the sole entry otherwise.
	effective := uids[0]
	if len(uids) > 1 {
		effective = uids[1]
	}
	return strconv.FormatUint(uint64(effective), 10) == r.currentUID, nil
}
