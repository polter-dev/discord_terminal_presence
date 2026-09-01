//go:build darwin

package detector

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

// ListIdentities returns only fields needed for registry matching.
func (*GopsutilLister) ListIdentities() ([]Process, error) {
	// This bulk path replicates gopsutil's Darwin Name semantics and may need
	// updating if a future gopsutil upgrade changes them.
	return listDarwinIdentities()
}

func listDarwinIdentities() ([]Process, error) {
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}

	euid := uint32(os.Geteuid())
	out := make([]Process, 0, len(kprocs))
	for i := range kprocs {
		kproc := &kprocs[i]
		// This is only an early application of the ownership boundary. The
		// authoritative resolver still checks every matched process later and
		// fails closed if ownership cannot be proven.
		if kproc.Eproc.Ucred.Uid != euid {
			continue
		}

		process := darwinProcessIdentity(kproc)
		if process.Name == "" && process.Exe == "" && process.Cmdline == "" && process.Argv0 == "" {
			continue
		}
		out = append(out, process)
	}
	// gopsutil's Pids path sorts ascending before constructing processes.
	sort.Slice(out, func(i, j int) bool { return out[i].Pid < out[j].Pid })
	return out, nil
}

func darwinProcessIdentity(kproc *unix.KinfoProc) Process {
	pid := int32(kproc.Proc.P_pid)
	millis := kproc.Proc.P_starttime.Sec*1000 + int64(kproc.Proc.P_starttime.Usec)/1000
	process := Process{Pid: pid}
	if millis > 0 {
		process.CreateTime = time.UnixMilli(millis)
	}
	comm := darwinKinfoString(kproc.Proc.P_comm[:])
	if len(comm) < 15 {
		process.Name = boundIdentityField(comm)
	}

	// Constructing Process directly avoids NewProcess's existence and creation-
	// time lookups; the bulk snapshot already supplied both pid and start time.
	proc := &psprocess.Process{Pid: pid}
	if args, err := proc.CmdlineSlice(); err == nil {
		if len(comm) >= 15 {
			process.Name = boundIdentityField(comm)
			if len(args) > 0 {
				if extended := filepath.Base(args[0]); extended != "" {
					process.Name = boundIdentityField(extended)
				}
			}
		}
		if len(args) > 0 {
			process.Argv0 = boundIdentityField(args[0])
			process.Argv = append([]string(nil), args...)
			process.Cmdline = boundIdentityField(strings.Join(args, " "))
		}
	}
	if resolved, err := proc.Exe(); err == nil {
		process.Exe = resolved
	}
	return process
}

// darwinKinfoString matches gopsutil's common.ByteToString behavior for the
// NUL-padded p_comm field without importing gopsutil's internal package.
func darwinKinfoString(value []byte) string {
	end := -1
	start := -1
	for i, b := range value {
		if start == -1 && b == 0 {
			continue
		}
		if start == -1 {
			start = i
		}
		if b == 0 {
			break
		}
		end = i + 1
	}
	if end == -1 {
		return string(value)
	}
	return string(value[start:end])
}
