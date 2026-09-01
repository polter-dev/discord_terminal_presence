//go:build !darwin

package detector

import psprocess "github.com/shirou/gopsutil/v4/process"

// ListIdentities returns only fields needed for registry matching.
func (*GopsutilLister) ListIdentities() ([]Process, error) {
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
