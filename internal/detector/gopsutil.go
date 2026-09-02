package detector

import (
	"strings"
	"sync"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	psprocess "github.com/shirou/gopsutil/v4/process"
)

// maxIdentityFieldBytes bounds Name, Argv0, and Cmdline before they reach the
// registry matcher. Catalog matching (registry.processMatchIdentity) only
// ever inspects process identity plus the immediate subcommand argument, so
// a few KiB is far more than any real tool invocation needs, while an
// unbounded flattened Cmdline is attacker-controlled: any process can set an
// arbitrarily long argv, and matching costs about 360ns/byte, so a handful of
// multi-MiB command lines can push one scan past the default scan interval
// (#565). The structured Argv slice is left intact for other consumers; the
// registry applies this same shared bound to the elements it promotes into
// entrypoint and subcommand matching (#603).
const maxIdentityFieldBytes = registry.MaxIdentityFieldBytes

// boundIdentityField truncates s to at most maxIdentityFieldBytes bytes
// without splitting a multi-byte UTF-8 rune.
func boundIdentityField(s string) string {
	return registry.BoundIdentityField(s)
}

// GopsutilLister reads processes through gopsutil.
type GopsutilLister struct {
	atimeOnce sync.Once
	atime     TTYAtimeSource

	episodesMu sync.RWMutex
	episodes   *EpisodeStore
}

// NewGopsutilLister returns a process lister whose terminal activity state is
// shared across scans.
func NewGopsutilLister() *GopsutilLister {
	return &GopsutilLister{}
}

// List returns a best-effort process snapshot. Individual fields may be unavailable.
func (*GopsutilLister) List() ([]Process, error) {
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

// Enrich resolves fields needed only after a process matches a known tool.
func (*GopsutilLister) Enrich(process Process) Process {
	proc, err := psprocess.NewProcess(process.Pid)
	if err != nil {
		return process
	}
	return enrichVerifyingInstance(proc, process)
}

// NewScanProcessEnricher shares tty and tmux snapshots across matched processes.
func (l *GopsutilLister) NewScanProcessEnricher() ProcessEnricher {
	enricher := &presenceProcessEnricher{
		base:     l,
		resolver: newSystemTTYResolver(),
		tmux:     &lazyTmuxPanes{query: queryTmuxPanes},
		atime:    l.ttyAtimeSource(),
		owner:    newSystemOwnerResolver(),
	}
	l.episodesMu.RLock()
	enricher.episodes = l.episodes
	l.episodesMu.RUnlock()
	return enricher
}

// setEpisodeStore supplies the daemon's loaded episode history to per-scan
// enrichers. The lister only reads it for first-observation focus fallback.
func (l *GopsutilLister) setEpisodeStore(episodes *EpisodeStore) {
	l.episodesMu.Lock()
	l.episodes = episodes
	l.episodesMu.Unlock()
}

func (l *GopsutilLister) ttyAtimeSource() TTYAtimeSource {
	l.atimeOnce.Do(func() {
		l.atime = newSystemTTYAtimeSource()
	})
	return l.atime
}

func processIdentity(proc *psprocess.Process) Process {
	process := Process{Pid: proc.Pid}
	// CreateTime is captured here, at identity time, so later independent
	// lookups of the same pid (enrichment, ownership) can be cross-checked
	// against it and reject a recycled pid rather than silently mixing
	// fields from two different OS process instances (#569).
	if millis, err := proc.CreateTime(); err == nil && millis > 0 {
		process.CreateTime = time.UnixMilli(millis)
	}
	if resolved, err := proc.Name(); err == nil {
		process.Name = boundIdentityField(resolved)
	}
	if resolved, err := proc.Exe(); err == nil {
		process.Exe = resolved
	}
	if args, err := proc.CmdlineSlice(); err == nil {
		if len(args) > 0 {
			process.Argv0 = boundIdentityField(args[0])
			process.Argv = append([]string(nil), args...)
			process.Cmdline = boundIdentityField(strings.Join(args, " "))
		}
	}
	if process.Cmdline == "" {
		if resolved, err := proc.Cmdline(); err == nil {
			process.Cmdline = boundIdentityField(resolved)
		}
	}
	return process
}

// enrichVerifyingInstance enriches process from a freshly opened handle for
// the same pid, but only accepts the result when the handle's creation time
// still matches the creation time captured at identity time. A mismatch
// means the pid was recycled between ListIdentities and this enrichment
// (#569): a foreign process could otherwise have exited and been replaced by
// one of the user's, publishing a foreign tool identity (matched from the
// exited process's name/argv) alongside the new process's cwd. When
// process.CreateTime is unknown (zero), the check is skipped rather than
// discarding enrichment, since there is nothing to compare against.
func enrichVerifyingInstance(proc *psprocess.Process, process Process) Process {
	enriched := enrichProcess(proc, process)
	if !process.CreateTime.IsZero() && !enriched.CreateTime.Equal(process.CreateTime) {
		return process
	}
	return enriched
}

func enrichProcess(proc *psprocess.Process, process Process) Process {
	if resolved, err := proc.Cwd(); err == nil {
		process.Cwd = resolved
	}
	if millis, err := proc.CreateTime(); err == nil && millis > 0 {
		process.CreateTime = time.UnixMilli(millis)
	}
	process.CPUTimeKnown = false
	if times, err := proc.Times(); err == nil && times != nil {
		process.CPUTime = times.User + times.System
		process.CPUTimeKnown = true
	}
	return process
}
