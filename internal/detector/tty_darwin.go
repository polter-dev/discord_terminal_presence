//go:build darwin

package detector

import (
	"errors"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type darwinTTYResolver struct {
	devices map[uint64]string
	err     error
}

func newSystemTTYResolver() TTYResolver {
	paths, err := filepath.Glob("/dev/tty*")
	resolver := &darwinTTYResolver{devices: make(map[uint64]string), err: err}
	if err != nil {
		return resolver
	}
	for _, path := range paths {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			continue
		}
		resolver.devices[uint64(stat.Rdev)] = path
	}
	return resolver
}

func (r *darwinTTYResolver) Resolve(pid int32) (TTYResolution, error) {
	if r.err != nil {
		return TTYResolution{}, r.err
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil {
		return TTYResolution{}, err
	}
	if process.Eproc.Tdev == -1 {
		return TTYResolution{NoTTY: true}, nil
	}
	path, ok := r.devices[uint64(process.Eproc.Tdev)]
	if !ok {
		return TTYResolution{}, errors.New("controlling tty device not found")
	}
	return TTYResolution{Path: path}, nil
}

type systemTTYAtimeSource struct{}

func (systemTTYAtimeSource) Atime(path string) (time.Time, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return time.Time{}, err
	}
	return time.Unix(stat.Atim.Sec, stat.Atim.Nsec), nil
}
