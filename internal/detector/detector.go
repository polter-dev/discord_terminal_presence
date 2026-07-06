package detector

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

const (
	DefaultScanInterval         = 3 * time.Second
	DefaultDebounceCycles       = 2
	DefaultHeadlinerIdleTimeout = time.Minute

	activityThreshold = 0.01
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
	CPUTime    float64
}

// ProcessLister abstracts process inspection so tests do not scan the real OS.
type ProcessLister interface {
	List() ([]Process, error)
}

// ProcessIdentityLister can provide cheap identity fields before expensive enrichment.
type ProcessIdentityLister interface {
	ListIdentities() ([]Process, error)
}

// ProcessEnricher fills expensive fields for a process that already matched a known tool.
type ProcessEnricher interface {
	Enrich(Process) Process
}

// FeaturedTool is the headliner tool selected for the primary Discord activity.
type FeaturedTool struct {
	Tool      registry.Tool
	Cwd       string
	StartedAt time.Time
}

// Detection is emitted when the detected tool collection changes. None means no known
// tool is active. Tool, Cwd, and StartedAt mirror Featured for existing callers.
type Detection struct {
	Featured  FeaturedTool
	Others    []registry.Tool
	Tool      registry.Tool
	Cwd       string
	StartedAt time.Time
	None      bool
}

// Config controls detector polling and debounce behavior.
type Config struct {
	ScanInterval         time.Duration
	DebounceCycles       int
	Pin                  string
	HeadlinerIdleTimeout time.Duration
	IdleClearTimeout     time.Duration
	ActivitySwitching    bool
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
	if !config.ActivitySwitching && config.Pin == "" && config.HeadlinerIdleTimeout <= 0 {
		config.ActivitySwitching = true
	}
	if config.HeadlinerIdleTimeout <= 0 {
		config.HeadlinerIdleTimeout = DefaultHeadlinerIdleTimeout
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
	return NewSelector(d.registry, d.config, systemClock{}).Select(processes)
}

// ActiveDetection returns the best detection from a process snapshot.
func ActiveDetection(reg *registry.Registry, processes []Process) Detection {
	return NewSelector(reg, Config{ActivitySwitching: true}, systemClock{}).Select(processes)
}

// Clock supplies time to Selector so headliner hysteresis is deterministic in tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

// Selector owns headliner activity/hysteresis state across process snapshots.
type Selector struct {
	registry *registry.Registry
	config   Config
	clock    Clock

	previousFeatured string
	previousCPU      map[string]float64
	idleSince        map[string]time.Time
}

// NewSelector creates a stateful selector for repeated snapshots.
func NewSelector(reg *registry.Registry, config Config, clock Clock) *Selector {
	if !config.ActivitySwitching && config.Pin == "" && config.HeadlinerIdleTimeout <= 0 {
		config.ActivitySwitching = true
	}
	if config.HeadlinerIdleTimeout <= 0 {
		config.HeadlinerIdleTimeout = DefaultHeadlinerIdleTimeout
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Selector{
		registry:    reg,
		config:      config,
		clock:       clock,
		previousCPU: make(map[string]float64),
		idleSince:   make(map[string]time.Time),
	}
}

// Select returns the collection snapshot for one process list.
func (s *Selector) Select(processes []Process) Detection {
	return s.SelectWithEnricher(processes, nil)
}

// SelectWithEnricher returns the collection snapshot, enriching only matched processes.
func (s *Selector) SelectWithEnricher(processes []Process, enricher ProcessEnricher) Detection {
	candidates := make(map[string]toolCandidate)
	cpuTotals := make(map[string]float64)
	for _, proc := range processes {
		tool, ok := s.registry.MatchProcess(registry.ProcessInfo{
			Name:    proc.Name,
			Exe:     proc.Exe,
			Cmdline: proc.Cmdline,
			Argv0:   proc.Argv0,
		})
		if !ok {
			continue
		}
		if enricher != nil {
			proc = enricher.Enrich(proc)
		}

		cpuTotals[tool.ID] += proc.CPUTime
		candidate := toolCandidate{
			FeaturedTool: FeaturedTool{
				Tool:      tool,
				Cwd:       proc.Cwd,
				StartedAt: proc.CreateTime,
			},
		}

		current, exists := candidates[tool.ID]
		if !exists || isBetterInstance(candidate.FeaturedTool, current.FeaturedTool) {
			candidates[tool.ID] = candidate
		}
	}

	if len(candidates) == 0 {
		s.previousFeatured = ""
		s.previousCPU = make(map[string]float64)
		s.idleSince = make(map[string]time.Time)
		return Detection{None: true}
	}

	now := s.clock.Now()
	for id, candidate := range candidates {
		activity := cpuTotals[id] - s.previousCPU[id]
		if activity < 0 {
			activity = 0
		}
		candidate.Activity = activity
		candidates[id] = candidate
		if activity >= activityThreshold {
			delete(s.idleSince, id)
		} else if _, ok := s.idleSince[id]; !ok {
			s.idleSince[id] = now
		}
	}
	s.previousCPU = cpuTotals
	for id := range s.idleSince {
		if _, running := candidates[id]; !running {
			delete(s.idleSince, id)
		}
	}

	// Idle clear is opt-in because CPU deltas can miss quiet interactive work
	// such as editing text without invoking CPU-heavy operations.
	if s.config.IdleClearTimeout > 0 && s.allRunningToolsIdle(candidates, now) {
		return Detection{None: true}
	}

	featured := s.selectFeatured(candidates, now)
	s.previousFeatured = featured.Tool.ID

	others := sortedOthers(candidates, featured.Tool.ID)
	return detectionFromFeatured(featured, others)
}

func (s *Selector) allRunningToolsIdle(candidates map[string]toolCandidate, now time.Time) bool {
	for id := range candidates {
		idleSince, idle := s.idleSince[id]
		if !idle || now.Sub(idleSince) < s.config.IdleClearTimeout {
			return false
		}
	}
	return true
}

func (s *Selector) selectFeatured(candidates map[string]toolCandidate, now time.Time) FeaturedTool {
	if s.config.Pin != "" {
		if pinned, ok := candidates[s.config.Pin]; ok {
			return pinned.FeaturedTool
		}
	}

	if previous, ok := candidates[s.previousFeatured]; ok {
		if !s.config.ActivitySwitching {
			return previous.FeaturedTool
		}
		challenger, ok := mostActive(candidates, s.previousFeatured)
		if !ok || challenger.Activity < activityThreshold || challenger.Activity <= previous.Activity+activityThreshold {
			return previous.FeaturedTool
		}
		idleSince, idle := s.idleSince[s.previousFeatured]
		if !idle || now.Sub(idleSince) < s.config.HeadlinerIdleTimeout {
			return previous.FeaturedTool
		}
		return challenger.FeaturedTool
	}

	var (
		best toolCandidate
		ok   bool
	)
	for _, candidate := range candidates {
		if !ok || isBetterActiveTool(candidate.FeaturedTool, best.FeaturedTool) {
			best = candidate
			ok = true
		}
	}
	return best.FeaturedTool
}

type toolCandidate struct {
	FeaturedTool
	Activity float64
}

func mostActive(candidates map[string]toolCandidate, excludeID string) (toolCandidate, bool) {
	var (
		best toolCandidate
		ok   bool
	)
	for id, candidate := range candidates {
		if id == excludeID {
			continue
		}
		if !ok || compareActivity(candidate, best) {
			best = candidate
			ok = true
		}
	}
	return best, ok
}

func sortedOthers(candidates map[string]toolCandidate, featuredID string) []registry.Tool {
	others := make([]toolCandidate, 0, len(candidates)-1)
	for id, candidate := range candidates {
		if id != featuredID {
			others = append(others, candidate)
		}
	}
	sort.SliceStable(others, func(i, j int) bool {
		return compareActivity(others[i], others[j])
	})
	out := make([]registry.Tool, 0, len(others))
	for _, candidate := range others {
		out = append(out, candidate.Tool)
	}
	return out
}

func compareActivity(left, right toolCandidate) bool {
	if left.Activity != right.Activity {
		return left.Activity > right.Activity
	}
	if left.Tool.Priority != right.Tool.Priority {
		return left.Tool.Priority > right.Tool.Priority
	}
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	return left.Tool.ID < right.Tool.ID
}

func detectionFromFeatured(featured FeaturedTool, others []registry.Tool) Detection {
	return Detection{
		Featured:  featured,
		Others:    append([]registry.Tool(nil), others...),
		Tool:      featured.Tool,
		Cwd:       featured.Cwd,
		StartedAt: featured.StartedAt,
	}
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
	selector := NewSelector(d.registry, d.config, systemClock{})

	scan := func() bool {
		processes, err := listProcesses(d.lister)
		if err != nil {
			processes = nil
		}
		current := selector.SelectWithEnricher(processes, processEnricher(d.lister))

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

func listProcesses(lister ProcessLister) ([]Process, error) {
	if identityLister, ok := lister.(ProcessIdentityLister); ok {
		return identityLister.ListIdentities()
	}
	return lister.List()
}

func processEnricher(lister ProcessLister) ProcessEnricher {
	enricher, _ := lister.(ProcessEnricher)
	return enricher
}

func isBetterInstance(left, right FeaturedTool) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	return left.Tool.Priority > right.Tool.Priority
}

func isBetterActiveTool(left, right FeaturedTool) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	return left.Tool.Priority > right.Tool.Priority
}

func sameDetection(left, right Detection) bool {
	if left.None || right.None {
		return left.None == right.None
	}
	if left.Tool.ID != right.Tool.ID ||
		left.Cwd != right.Cwd ||
		!left.StartedAt.Equal(right.StartedAt) {
		return false
	}
	if len(left.Others) != len(right.Others) {
		return false
	}
	for i := range left.Others {
		if left.Others[i].ID != right.Others[i].ID {
			return false
		}
	}
	return true
}
