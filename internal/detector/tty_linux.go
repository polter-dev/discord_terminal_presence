//go:build linux

package detector

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func newSystemTTYResolver() TTYResolver {
	resolver := &linuxTTYResolver{
		devices: make(map[uint64]string),
		readStat: func(pid int32) ([]byte, error) {
			return os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		},
	}
	for _, pattern := range []string{"/dev/pts/*", "/dev/tty*"} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			resolver.err = err
			return resolver
		}
		for _, path := range paths {
			var stat unix.Stat_t
			if err := unix.Stat(path, &stat); err != nil {
				continue
			}
			resolver.devices[uint64(stat.Rdev)] = path
		}
	}
	return resolver
}

func newSystemTTYAtimeSource() TTYAtimeSource {
	contents, err := os.ReadFile("/proc/mounts")
	mounts, parseErr := parseLinuxMounts(contents)
	if err == nil {
		err = parseErr
	}
	return linuxTTYAtimeSource{
		mounts:   mounts,
		mountErr: err,
		stat: func(path string) (time.Time, error) {
			var stat unix.Stat_t
			if err := unix.Stat(path, &stat); err != nil {
				return time.Time{}, err
			}
			return time.Unix(stat.Atim.Sec, stat.Atim.Nsec), nil
		},
	}
}
