package detector

import (
	"context"
	"errors"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

const (
	DefaultScanInterval   = 3 * time.Second
	DefaultDebounceCycles = 2
)

// Process is the detector's small, testable view of an OS process.
type Process struct {
	Pid        int32
	Name       string
	Exe        string
	Cmdline    string
	Argv0      string
	Cwd        string
	CreateTime time.Time
}

// ProcessLister abstracts process inspection so tests do not scan the real OS.
type ProcessLister interface {
	List() ([]Process, error)
}

// Detection is emitted when the active tool changes. None means no known tool is active.
type Detection struct {
	Tool      registry.Tool
	Cwd       string
	StartedAt time.Time
	None      bool
}

// Config controls detector polling and debounce behavior.
type Config struct {
	ScanInterval   time.Duration
	DebounceCycles int
}

// Detector owns the process scan loop.
type Detector struct {
	registry *registry.Registry
	lister   ProcessLister
	config   Config
}

// New creates a detector with explicit dependencies.
func New(reg *registry.Registry, lister ProcessLister, config Config) (*Detector, error) {
	if reg == nil {
		return nil, errors.New("detector: registry is required")
	}
	if lister == nil {
		return nil, errors.New("detector: process lister is required")
	}
	if config.ScanInterval <= 0 {
		config.ScanInterval = DefaultScanInterval
	}
	if config.DebounceCycles <= 0 {
		config.DebounceCycles = DefaultDebounceCycles
	}
	return &Detector{registry: reg, lister: lister, config: config}, nil
}

// Run starts one goroutine that scans until ctx is cancelled.
func (d *Detector) Run(ctx context.Context) <-chan Detection {
	out := make(chan Detection, 1)
	go d.run(ctx, out)
	return out
}

// ActiveDetection returns the current best detection from a process snapshot.
func (d *Detector) ActiveDetection(processes []Process) Detection {
	return ActiveDetection(d.registry, processes)
}

// ActiveDetection returns the best detection from a process snapshot.
func ActiveDetection(reg *registry.Registry, processes []Process) Detection {
	candidates := make(map[string]Detection)
	for _, proc := range processes {
		tool, ok := reg.MatchProcess(registry.ProcessInfo{
			Name:    proc.Name,
			Exe:     proc.Exe,
			Cmdline: proc.Cmdline,
			Argv0:   proc.Argv0,
		})
		if !ok {
			continue
		}

		detection := Detection{
			Tool:      tool,
			Cwd:       proc.Cwd,
			StartedAt: proc.CreateTime,
		}

		current, exists := candidates[tool.ID]
		if !exists || isBetterInstance(detection, current) {
			candidates[tool.ID] = detection
		}
	}

	if len(candidates) == 0 {
		return Detection{None: true}
	}

	var (
		best Detection
		ok   bool
	)
	for _, detection := range candidates {
		if !ok || isBetterActiveTool(detection, best) {
			best = detection
			ok = true
		}
	}
	return best
}

func (d *Detector) run(ctx context.Context, out chan<- Detection) {
	defer close(out)

	var (
		emitted      Detection
		hasEmitted   bool
		candidate    Detection
		candidateSet bool
		streak       int
	)

	scan := func() bool {
		processes, err := d.lister.List()
		if err != nil {
			processes = nil
		}
		current := ActiveDetection(d.registry, processes)

		if !candidateSet || !sameDetection(current, candidate) {
			candidate = current
			candidateSet = true
			streak = 1
		} else {
			streak++
		}

		if streak < d.config.DebounceCycles {
			return true
		}
		if hasEmitted && sameDetection(candidate, emitted) {
			return true
		}

		select {
		case out <- candidate:
			emitted = candidate
			hasEmitted = true
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !scan() {
		return
	}

	ticker := time.NewTicker(d.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !scan() {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func isBetterInstance(left, right Detection) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	return left.Tool.Priority > right.Tool.Priority
}

func isBetterActiveTool(left, right Detection) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	return left.Tool.Priority > right.Tool.Priority
}

func sameDetection(left, right Detection) bool {
	if left.None || right.None {
		return left.None == right.None
	}
	return left.Tool.ID == right.Tool.ID &&
		left.Cwd == right.Cwd &&
		left.StartedAt.Equal(right.StartedAt)
}
