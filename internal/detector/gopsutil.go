package detector

import (
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
		var name string
		if resolved, err := proc.Name(); err == nil {
			name = resolved
		}

		var exe string
		if resolved, err := proc.Exe(); err == nil {
			exe = resolved
		}

		var cmdline string
		if resolved, err := proc.Cmdline(); err == nil {
			cmdline = resolved
		}

		var argv0 string
		if args, err := proc.CmdlineSlice(); err == nil && len(args) > 0 {
			argv0 = args[0]
		}

		if name == "" && exe == "" && cmdline == "" && argv0 == "" {
			continue
		}

		var cwd string
		if resolved, err := proc.Cwd(); err == nil {
			cwd = resolved
		}

		var createTime time.Time
		if millis, err := proc.CreateTime(); err == nil && millis > 0 {
			createTime = time.UnixMilli(millis)
		}

		out = append(out, Process{
			Pid:        proc.Pid,
			Name:       name,
			Exe:        exe,
			Cmdline:    cmdline,
			Argv0:      argv0,
			Cwd:        cwd,
			CreateTime: createTime,
		})
	}

	return out, nil
}
