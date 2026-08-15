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

// OwnerResolver decides whether a process belongs to the current effective
// user. An error means ownership could not be established (permission
// denied, process exited mid-scan, platform unsupported); callers must treat
// that identically to "not owned" rather than defaulting to include the
// process. See Process.Owned.
//
// createTime, when non-zero, is the process creation time captured at
// identity time (ListIdentities). Implementations that can cheaply verify it
// against the pid's current creation time must do so and fail (return an
// error) on a mismatch: a mismatch means the pid was recycled between
// identity capture and this ownership check (#569), so pid alone no longer
// names the same OS process instance. A zero createTime skips that check.
type OwnerResolver interface {
	Owned(pid int32, createTime time.Time) (bool, error)
}

type episodeTTYAtimeSource interface {
	AtimeWithEpisode(tty string, lastAtime time.Time, episodeExists bool) (time.Time, error)
}

type focusedTTYAtimeSource interface {
	AtimeWithFocus(tty string, lastAtime time.Time, episodeExists bool) (time.Time, bool, error)
}

type presenceProcessEnricher struct {
	base     ProcessEnricher
	resolver TTYResolver
	tmux     TmuxPaneSnapshot
	atime    TTYAtimeSource
	owner    OwnerResolver
	episodes *EpisodeStore
}

func newPresenceProcessEnricher(base ProcessEnricher, resolver TTYResolver, tmux TmuxPaneSnapshot, atime TTYAtimeSource, owner OwnerResolver) ProcessEnricher {
	return &presenceProcessEnricher{base: base, resolver: resolver, tmux: tmux, atime: atime, owner: owner}
}

func (e *presenceProcessEnricher) Enrich(process Process) Process {
	return e.enrich(process, "")
}

func (e *presenceProcessEnricher) EnrichMatched(process Process, toolID string) Process {
	return e.enrich(process, toolID)
}

func (e *presenceProcessEnricher) enrich(process Process, toolID string) Process {
	if e.base != nil {
		process = e.base.Enrich(process)
	}
	// Resolved independently of TTY: ownership must exclude a foreign
	// process even when it has no controlling terminal information at all,
	// and must never be skipped just because TTY resolution below returns
	// early (no resolver, no TTY, unresolved path).
	process.Owned = false
	if e.owner != nil {
		if owned, err := e.owner.Owned(process.Pid, process.CreateTime); err == nil {
			process.Owned = owned
		}
	}
	// Ownership is now known. Everything below (TTY resolve, tmux query,
	// atime stat, and on Windows a spawned console-probe child) is expensive
	// per-candidate work that the selector's ownership gate at detector.go
	// discards for any foreign process. Bail here, after ownership is
	// resolved (never before it, and never skipping it), so that discarded
	// work is never done at all (#566).
	if !process.Owned {
		return process
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
		var (
			atime      time.Time
			foreground bool
			focusKnown bool
			err        error
		)
		lastAtime, episodeExists := time.Time{}, false
		if e.episodes != nil && toolID != "" {
			lastAtime, episodeExists = e.episodes.LastAtime(EpisodeKey(toolID, process.Pid, process.CreateTime))
		}
		if source, ok := e.atime.(focusedTTYAtimeSource); ok {
			atime, foreground, err = source.AtimeWithFocus(process.TTY.Path, lastAtime, episodeExists)
			focusKnown = err == nil
		} else if source, ok := e.atime.(episodeTTYAtimeSource); ok && e.episodes != nil && toolID != "" {
			atime, err = source.AtimeWithEpisode(process.TTY.Path, lastAtime, episodeExists)
		} else {
			atime, err = e.atime.Atime(process.TTY.Path)
		}
		if err == nil {
			process.TTY.Atime = atime
			process.TTY.AtimeKnown = true
			process.TTY.Foreground = foreground
			process.TTY.FocusKnown = focusKnown
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
