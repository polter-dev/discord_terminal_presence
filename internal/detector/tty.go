package detector

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const tmuxQueryTimeout = 250 * time.Millisecond

type TTYResolution struct {
	Path  string
	NoTTY bool
}

type TTYResolver interface {
	Resolve(pid int32) (TTYResolution, error)
}

type TmuxPaneSnapshot interface {
	Detached(tty string) (detached bool, matched bool)
}

type TTYAtimeSource interface {
	Atime(tty string) (time.Time, error)
}

type presenceProcessEnricher struct {
	base     ProcessEnricher
	resolver TTYResolver
	tmux     TmuxPaneSnapshot
	atime    TTYAtimeSource
}

func newPresenceProcessEnricher(base ProcessEnricher, resolver TTYResolver, tmux TmuxPaneSnapshot, atime TTYAtimeSource) ProcessEnricher {
	return &presenceProcessEnricher{base: base, resolver: resolver, tmux: tmux, atime: atime}
}

func (e *presenceProcessEnricher) Enrich(process Process) Process {
	if e.base != nil {
		process = e.base.Enrich(process)
	}
	if e.resolver == nil {
		return process
	}
	resolved, err := e.resolver.Resolve(process.Pid)
	if err != nil {
		return process
	}
	if resolved.NoTTY {
		process.TTY.State = TTYNone
		return process
	}
	if resolved.Path == "" {
		return process
	}
	process.TTY.State = TTYResolved
	process.TTY.Path = filepath.Clean(resolved.Path)
	if e.tmux != nil {
		if detached, matched := e.tmux.Detached(process.TTY.Path); matched && detached {
			process.TTY.DetachedTmux = true
		}
	}
	if e.atime != nil {
		if atime, err := e.atime.Atime(process.TTY.Path); err == nil {
			process.TTY.Atime = atime
			process.TTY.AtimeKnown = true
		}
	}
	return process
}

type tmuxPanes struct {
	attached map[string]bool
	err      error
}

func queryTmuxPanes() TmuxPaneSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxQueryTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_tty}\t#{session_attached}").Output()
	if err != nil {
		return &tmuxPanes{err: err}
	}
	panes, err := parseTmuxPanes(string(output))
	if err != nil {
		return &tmuxPanes{err: err}
	}
	return panes
}

func parseTmuxPanes(output string) (*tmuxPanes, error) {
	panes := &tmuxPanes{attached: make(map[string]bool)}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || fields[0] == "" {
			return nil, errors.New("malformed tmux pane snapshot")
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil || count < 0 {
			return nil, errors.New("malformed tmux attached count")
		}
		path := filepath.Clean(fields[0])
		panes.attached[path] = panes.attached[path] || count > 0
	}
	return panes, nil
}

func (p *tmuxPanes) Detached(tty string) (bool, bool) {
	if p == nil || p.err != nil {
		return false, false
	}
	attached, ok := p.attached[filepath.Clean(tty)]
	return ok && !attached, ok
}
