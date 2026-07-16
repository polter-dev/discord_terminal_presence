package detector

import (
	"strings"
	"time"

	psprocess "github.com/shirou/gopsutil/v4/process"
)

// GopsutilLister reads processes through gopsutil.
type GopsutilLister struct{}

// List returns a best-effort process snapshot. Individual fields may be unavailable.
func (GopsutilLister) List() ([]Process, error) {
	processes, err := psprocess.Processes()
	if err != nil {
		return nil, err
	}

	out := make([]Process, 0, len(processes))
	for _, proc := range processes {
		process := processIdentity(proc)
		if process.Name == "" && process.Exe == "" && process.Cmdline == "" && process.Argv0 == "" {
			continue
		}

		out = append(out, enrichProcess(proc, process))
	}

	return out, nil
}

// ListIdentities returns only fields needed for registry matching.
func (GopsutilLister) ListIdentities() ([]Process, error) {
	processes, err := psprocess.Processes()
	if err != nil {
		return nil, err
	}

	out := make([]Process, 0, len(processes))
	for _, proc := range processes {
		process := processIdentity(proc)
		if process.Name == "" && process.Exe == "" && process.Cmdline == "" && process.Argv0 == "" {
			continue
		}
		out = append(out, process)
	}
	return out, nil
}

// Enrich resolves fields needed only after a process matches a known tool.
func (GopsutilLister) Enrich(process Process) Process {
	proc, err := psprocess.NewProcess(process.Pid)
	if err != nil {
		return process
	}
	return enrichProcess(proc, process)
}

// NewScanProcessEnricher shares tty and tmux snapshots across matched processes.
func (l GopsutilLister) NewScanProcessEnricher() ProcessEnricher {
	return newPresenceProcessEnricher(l, newSystemTTYResolver(), queryTmuxPanes(), newSystemTTYAtimeSource())
}

func processIdentity(proc *psprocess.Process) Process {
	process := Process{Pid: proc.Pid}
	if resolved, err := proc.Name(); err == nil {
		process.Name = resolved
	}
	if resolved, err := proc.Exe(); err == nil {
		process.Exe = resolved
	}
	if args, err := proc.CmdlineSlice(); err == nil {
		if len(args) > 0 {
			process.Argv0 = args[0]
			process.Cmdline = strings.Join(args, " ")
		}
	}
	if process.Cmdline == "" {
		if resolved, err := proc.Cmdline(); err == nil {
			process.Cmdline = resolved
		}
	}
	return process
}

func enrichProcess(proc *psprocess.Process, process Process) Process {
	if resolved, err := proc.Cwd(); err == nil {
		process.Cwd = resolved
	}
	if millis, err := proc.CreateTime(); err == nil && millis > 0 {
		process.CreateTime = time.UnixMilli(millis)
	}
	if times, err := proc.Times(); err == nil && times != nil {
		process.CPUTime = times.User + times.System
	}
	return process
}
