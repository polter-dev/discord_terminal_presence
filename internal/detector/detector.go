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

	activityThreshold           = 0.01
	instanceSwitchConfirmations = 2
)

// Process is the detector's small, testable view of an OS process.
type Process struct {
	Pid          int32
	Name         string
	Exe          string
	Cmdline      string
	Argv0        string
	Argv         []string
	Cwd          string
	CreateTime   time.Time
	CPUTime      float64
	CPUTimeKnown bool
	TTY          TTYInfo
	// Owned is true only when ownership resolution affirmatively proved this
	// process belongs to the current effective user. It defaults to false and
	// stays false whenever resolution did not run or failed (permission
	// denied, process exited mid-scan, platform unsupported): the selector
	// treats "not proven mine" the same as "proven someone else's" and
	// excludes the process. See presenceEligible's neighboring ownership
	// check in Select/SelectWithEnricher, which is the single choke point
	// every candidate passes through regardless of which set (featured or
	// collection) it is being considered for.
	Owned bool
}

// TTYState distinguishes a definitive lack of a controlling tty from an
// inspection failure. Unknown always fails open.
type TTYState uint8

const (
	TTYUnknown TTYState = iota
	TTYResolved
	TTYNone
)

// TTYInfo is the per-scan controlling-terminal presence snapshot for a process.
type TTYInfo struct {
	State        TTYState
	Path         string
	Atime        time.Time
	AtimeKnown   bool
	Foreground   bool
	FocusKnown   bool
	DetachedTmux bool
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

type matchedProcessEnricher interface {
	EnrichMatched(Process, string) Process
}

type episodeStoreConsumer interface {
	setEpisodeStore(*EpisodeStore)
}

// ScanProcessEnricherFactory builds an enricher whose expensive snapshots are
// shared by all matched processes in one scan.
type ScanProcessEnricherFactory interface {
	NewScanProcessEnricher() ProcessEnricher
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
	ScanInterval           time.Duration
	DebounceCycles         int
	Pin                    string
	HeadlinerIdleTimeout   time.Duration
	IdleClearTimeout       time.Duration
	CorroborateIdleWithCPU bool
	ActivitySwitching      bool
	DisabledTools          map[string]bool
}

// ScanFailureClearThreshold is the number of consecutive process-list failures
// tolerated before the detector clears a previously emitted presence.
const ScanFailureClearThreshold = 3

// Detector owns the process scan loop.
type Detector struct {
	registry          *registry.Registry
	lister            ProcessLister
	config            Config
	debugf            func(string, ...any)
	presenceStatePath string
	reconfigure       chan reconfigureRequest
}

type reconfigureRequest struct {
	registry *registry.Registry
	config   Config
	done     chan struct{}
}

// New creates a detector with explicit dependencies.
func New(reg *registry.Registry, lister ProcessLister, config Config) (*Detector, error) {
	if reg == nil {
		return nil, errors.New("detector: registry is required")
	}
	if lister == nil {
		return nil, errors.New("detector: process lister is required")
	}
	config = normalizeConfig(config)
	return &Detector{
		registry:          reg,
		lister:            lister,
		config:            config,
		debugf:            func(string, ...any) {},
		presenceStatePath: EpisodeStatePath(),
		reconfigure:       make(chan reconfigureRequest),
	}, nil
}

// SetDebugf enables optional diagnostic logging for process scans and detection decisions.
func (d *Detector) SetDebugf(debugf func(string, ...any)) {
	if debugf != nil {
		d.debugf = debugf
	}
}

// Reconfigure applies a new registry and detector config to a running detector.
// Selector history and presence episodes are preserved; the next scan is
// immediate and is emitted even when the selected tool IDs did not change.
func (d *Detector) Reconfigure(ctx context.Context, reg *registry.Registry, config Config) error {
	if reg == nil {
		return errors.New("detector: registry is required")
	}
	request := reconfigureRequest{
		registry: reg,
		config:   normalizeConfig(config),
		done:     make(chan struct{}),
	}
	select {
	case d.reconfigure <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-request.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func normalizeConfig(config Config) Config {
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
	return config
}

// Run starts one goroutine that scans until ctx is cancelled.
func (d *Detector) Run(ctx context.Context) <-chan Detection {
	out := make(chan Detection, 1)
	go d.run(ctx, out, SaveEpisodeStore)
	return out
}

// RunReadOnly starts one goroutine that loads presence episodes but never saves them.
func (d *Detector) RunReadOnly(ctx context.Context) <-chan Detection {
	out := make(chan Detection, 1)
	go d.run(ctx, out, nil)
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

// ActiveDetectionWithPresence scans once with the same presence eligibility and
// episode anchors as the daemon. The loaded episode store is never saved.
func ActiveDetectionWithPresence(reg *registry.Registry, lister ProcessLister, config Config) (Detection, error) {
	detector, err := New(reg, lister, config)
	if err != nil {
		return Detection{}, err
	}
	processes, err := listProcesses(lister)
	if err != nil {
		return Detection{}, err
	}
	episodes, _ := LoadEpisodeStore(EpisodeStatePath())
	if consumer, ok := lister.(episodeStoreConsumer); ok {
		consumer.setEpisodeStore(episodes)
	}
	selector := newSelectorWithEpisodes(detector.registry, detector.config, systemClock{}, episodes, nil)
	return selector.SelectWithEnricher(processes, processEnricher(lister)), nil
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

	previousFeatured   string
	previousCPU        map[string]float64
	idleSince          map[string]time.Time
	processCPU         map[string]processCPUObservation
	selectedInstances  map[string]string
	instanceChallenges map[string]instanceChallenge
	episodes           *EpisodeStore
	saveEpisodes       func(*EpisodeStore)
}

type processCPUObservation struct {
	total       float64
	lastChanged time.Time
	hasChanged  bool
	available   bool
}

type instanceChallenge struct {
	key           string
	confirmations int
}

// NewSelector creates a stateful selector for repeated snapshots.
func NewSelector(reg *registry.Registry, config Config, clock Clock) *Selector {
	config = normalizeConfig(config)
	if clock == nil {
		clock = systemClock{}
	}
	return newSelectorWithEpisodes(reg, config, clock, NewEpisodeStore(), nil)
}

// Reconfigure changes matching and selection settings without discarding
// headliner hysteresis, CPU history, idle timers, or presence episodes.
func (s *Selector) Reconfigure(reg *registry.Registry, config Config) {
	if reg == nil {
		return
	}
	s.registry = reg
	s.config = normalizeConfig(config)
}

func newSelectorWithEpisodes(reg *registry.Registry, config Config, clock Clock, episodes *EpisodeStore, save func(*EpisodeStore)) *Selector {
	if episodes == nil {
		episodes = NewEpisodeStore()
	}
	return &Selector{
		registry:           reg,
		config:             config,
		clock:              clock,
		previousCPU:        make(map[string]float64),
		idleSince:          make(map[string]time.Time),
		processCPU:         make(map[string]processCPUObservation),
		selectedInstances:  make(map[string]string),
		instanceChallenges: make(map[string]instanceChallenge),
		episodes:           episodes,
		saveEpisodes:       save,
	}
}

// Select returns the collection snapshot for one process list.
func (s *Selector) Select(processes []Process) Detection {
	return s.SelectWithEnricher(processes, nil)
}

// SelectWithEnricher returns the collection snapshot, enriching only matched processes.
func (s *Selector) SelectWithEnricher(processes []Process, enricher ProcessEnricher) Detection {
	candidateInstances := make(map[string][]toolCandidate)
	collectionInstances := make(map[string][]toolCandidate)
	cpuTotals := make(map[string]float64)
	now := s.clock.Now()
	eligibleEpisodes := make(map[string]struct{})
	observedProcesses := make(map[string]struct{})
	episodesChanged := false
	for _, proc := range processes {
		tool, ok := s.registry.MatchProcess(registry.ProcessInfo{
			Name:    proc.Name,
			Exe:     proc.Exe,
			Cmdline: proc.Cmdline,
			Argv0:   proc.Argv0,
			Argv:    proc.Argv,
		})
		if !ok {
			continue
		}
		if s.config.DisabledTools[tool.ID] {
			continue
		}
		if enricher != nil {
			if matched, ok := enricher.(matchedProcessEnricher); ok {
				proc = matched.EnrichMatched(proc, tool.ID)
			} else {
				proc = enricher.Enrich(proc)
			}
		}
		// Ownership gate: excludes every candidate not affirmatively proven to
		// belong to the current effective user, before it can reach either the
		// featured-eligibility path or the inactive-collection path below. This
		// is deliberately upstream of both so a future third selection path
		// inherits the filter automatically rather than needing its own check.
		if !proc.Owned {
			continue
		}
		episodeKey := EpisodeKey(tool.ID, proc.Pid, proc.CreateTime)
		processActivity, cpuActivityAt, cpuActivityKnown := s.observeProcessCPU(episodeKey, proc.CPUTime, proc.CPUTimeKnown, now)
		observedProcesses[episodeKey] = struct{}{}
		featuredEligible := s.presenceEligible(proc, episodeKey, now)
		if !featuredEligible && !inactiveCollectionEligible(proc) {
			continue
		}

		startedAt, changed := s.episodes.Observe(episodeKey, proc.TTY, now, s.config.IdleClearTimeout)
		eligibleEpisodes[episodeKey] = struct{}{}
		episodesChanged = episodesChanged || changed

		candidate := toolCandidate{
			FeaturedTool: FeaturedTool{
				Tool:      tool,
				Cwd:       proc.Cwd,
				StartedAt: startedAt,
			},
			CreateTime:       proc.CreateTime,
			InstanceKey:      episodeKey,
			ProcessActivity:  processActivity,
			CPUActivityAt:    cpuActivityAt,
			CPUActivityKnown: cpuActivityKnown,
			TTYActivityAt:    proc.TTY.Atime,
			TTYActivityKnown: proc.TTY.AtimeKnown,
		}

		collectionInstances[tool.ID] = append(collectionInstances[tool.ID], candidate)
		if !featuredEligible {
			continue
		}
		cpuTotals[tool.ID] += proc.CPUTime
		candidateInstances[tool.ID] = append(candidateInstances[tool.ID], candidate)
	}
	for key := range s.processCPU {
		if _, observed := observedProcesses[key]; !observed {
			delete(s.processCPU, key)
		}
	}
	episodesChanged = s.episodes.EndAbsent(eligibleEpisodes) || episodesChanged
	if episodesChanged && s.saveEpisodes != nil {
		s.saveEpisodes(s.episodes)
	}

	candidates := make(map[string]toolCandidate, len(candidateInstances))
	for id, instances := range candidateInstances {
		candidates[id] = s.selectInstance(id, instances, now)
	}
	for id := range s.selectedInstances {
		if _, running := candidateInstances[id]; !running {
			delete(s.selectedInstances, id)
			delete(s.instanceChallenges, id)
		}
	}

	if len(candidates) == 0 {
		s.previousFeatured = ""
		s.previousCPU = make(map[string]float64)
		s.idleSince = make(map[string]time.Time)
		return Detection{None: true}
	}

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

	featured := s.selectFeatured(candidates, now)
	s.previousFeatured = featured.Tool.ID

	collectionCandidates := make(map[string]toolCandidate, len(collectionInstances))
	for id, instances := range collectionInstances {
		if featuredCandidate, ok := candidates[id]; ok {
			collectionCandidates[id] = featuredCandidate
			continue
		}
		collectionCandidates[id] = s.bestInstance(instances, now)
	}
	others := s.sortedOthers(collectionCandidates, featured.Tool.ID)
	return detectionFromFeatured(featured, others)
}

func (s *Selector) observeProcessCPU(key string, total float64, available bool, now time.Time) (float64, time.Time, bool) {
	observation, ok := s.processCPU[key]
	if !available {
		observation.available = false
		s.processCPU[key] = observation
		return 0, time.Time{}, false
	}
	if !ok || !observation.available {
		s.processCPU[key] = processCPUObservation{total: total, lastChanged: now, available: true}
		return 0, time.Time{}, false
	}
	activity := total - observation.total
	if activity < 0 {
		activity = 0
	}
	if total > observation.total {
		observation.lastChanged = now
		observation.hasChanged = true
	}
	observation.total = total
	observation.available = true
	s.processCPU[key] = observation
	return activity, observation.lastChanged, observation.hasChanged
}

func (s *Selector) selectInstance(toolID string, instances []toolCandidate, now time.Time) toolCandidate {
	best := s.bestInstance(instances, now)
	selectedKey := s.selectedInstances[toolID]
	if selectedKey == "" {
		s.selectedInstances[toolID] = best.InstanceKey
		return best
	}

	var current toolCandidate
	currentFound := false
	for _, instance := range instances {
		if instance.InstanceKey == selectedKey {
			current = instance
			currentFound = true
			break
		}
	}
	if !currentFound {
		s.selectedInstances[toolID] = best.InstanceKey
		delete(s.instanceChallenges, toolID)
		return best
	}
	if best.InstanceKey == current.InstanceKey || !s.clearlyBetterInstance(best, current, now) {
		delete(s.instanceChallenges, toolID)
		return current
	}

	challenge := s.instanceChallenges[toolID]
	if challenge.key != best.InstanceKey {
		challenge = instanceChallenge{key: best.InstanceKey}
	}
	challenge.confirmations++
	if challenge.confirmations < instanceSwitchConfirmations {
		s.instanceChallenges[toolID] = challenge
		return current
	}

	s.selectedInstances[toolID] = best.InstanceKey
	delete(s.instanceChallenges, toolID)
	return best
}

func (s *Selector) bestInstance(instances []toolCandidate, now time.Time) toolCandidate {
	best := instances[0]
	for _, candidate := range instances[1:] {
		if s.isBetterInstance(candidate, best, now) {
			best = candidate
		}
	}
	return best
}

func (s *Selector) isBetterInstance(left, right toolCandidate, now time.Time) bool {
	if left.ProcessActivity > right.ProcessActivity+activityThreshold {
		return true
	}
	if right.ProcessActivity > left.ProcessActivity+activityThreshold {
		return false
	}
	leftActivity, leftActive := s.instanceActivityAt(left, now)
	rightActivity, rightActive := s.instanceActivityAt(right, now)
	if leftActive != rightActive {
		return leftActive
	}
	if leftActive && !leftActivity.Equal(rightActivity) {
		return leftActivity.After(rightActivity)
	}
	return isNewerInstance(left, right)
}

func (s *Selector) clearlyBetterInstance(challenger, current toolCandidate, now time.Time) bool {
	if challenger.ProcessActivity > current.ProcessActivity+activityThreshold {
		return true
	}
	challengerActivity, challengerActive := s.instanceActivityAt(challenger, now)
	currentActivity, currentActive := s.instanceActivityAt(current, now)
	if !challengerActive {
		return false
	}
	if !currentActive {
		return true
	}
	return challengerActivity.Sub(currentActivity) >= s.config.ScanInterval
}

func (s *Selector) instanceActivityAt(candidate toolCandidate, now time.Time) (time.Time, bool) {
	var latest time.Time
	if candidate.CPUActivityKnown {
		latest = candidate.CPUActivityAt
	}
	if candidate.TTYActivityKnown && candidate.TTYActivityAt.After(latest) {
		latest = candidate.TTYActivityAt
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	age := now.Sub(latest)
	if age < 0 {
		age = 0
	}
	return latest, age < s.config.HeadlinerIdleTimeout
}

func (s *Selector) presenceEligible(proc Process, episodeKey string, now time.Time) bool {
	if proc.TTY.State == TTYNone || proc.TTY.DetachedTmux {
		return false
	}
	if proc.TTY.State != TTYResolved || s.config.IdleClearTimeout <= 0 {
		return true
	}
	if !proc.TTY.AtimeKnown {
		observation, ok := s.processCPU[episodeKey]
		if !ok || !observation.available {
			return true
		}
		cpuAge := now.Sub(observation.lastChanged)
		return cpuAge < 0 || cpuAge < s.config.IdleClearTimeout
	}
	observation, ok := s.processCPU[episodeKey]
	cpuRecent := true
	if ok && observation.available {
		cpuAge := now.Sub(observation.lastChanged)
		cpuRecent = cpuAge < 0 || cpuAge < s.config.IdleClearTimeout
	}
	if s.config.CorroborateIdleWithCPU && proc.TTY.FocusKnown && proc.TTY.Foreground {
		return cpuRecent
	}
	age := now.Sub(proc.TTY.Atime)
	if age >= s.config.IdleClearTimeout {
		return false
	}
	if !s.config.CorroborateIdleWithCPU {
		return true
	}
	return cpuRecent
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
		if !ok || isBetterActiveCandidate(candidate, best) {
			best = candidate
			ok = true
		}
	}
	return best.FeaturedTool
}

func isBetterActiveCandidate(left, right toolCandidate) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	if left.Tool.Priority != right.Tool.Priority {
		return left.Tool.Priority > right.Tool.Priority
	}
	if !left.CreateTime.Equal(right.CreateTime) {
		return left.CreateTime.After(right.CreateTime)
	}
	return left.Tool.ID < right.Tool.ID
}

type toolCandidate struct {
	FeaturedTool
	Activity         float64
	CreateTime       time.Time
	InstanceKey      string
	ProcessActivity  float64
	CPUActivityAt    time.Time
	CPUActivityKnown bool
	TTYActivityAt    time.Time
	TTYActivityKnown bool
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

func (s *Selector) sortedOthers(candidates map[string]toolCandidate, featuredID string) []registry.Tool {
	others := make([]toolCandidate, 0, len(candidates)-1)
	for id, candidate := range candidates {
		if id == featuredID {
			continue
		}
		others = append(others, candidate)
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

func (d *Detector) run(ctx context.Context, out chan<- Detection, persistEpisodes func(string, *EpisodeStore) error) {
	defer close(out)

	var (
		emitted      Detection
		hasEmitted   bool
		candidate    Detection
		candidateSet bool
		streak       int
		scanFailures int
	)
	statePath := d.presenceStatePath
	episodes, err := LoadEpisodeStore(statePath)
	if err != nil {
		d.debugf("episode store load failed: %v", err)
	}
	if consumer, ok := d.lister.(episodeStoreConsumer); ok {
		consumer.setEpisodeStore(episodes)
	}
	var saveEpisodes func(*EpisodeStore)
	if persistEpisodes != nil {
		saveEpisodes = func(store *EpisodeStore) {
			if err := persistEpisodes(statePath, store); err != nil {
				d.debugf("episode store save failed: %v", err)
			}
		}
	}
	selector := newSelectorWithEpisodes(d.registry, d.config, systemClock{}, episodes, saveEpisodes)
	if saveEpisodes != nil {
		defer saveEpisodes(episodes)
	}

	var ticker *time.Ticker
	applyReconfigure := func(request reconfigureRequest) {
		intervalChanged := request.config.ScanInterval != d.config.ScanInterval
		d.registry = request.registry
		d.config = request.config
		selector.Reconfigure(request.registry, request.config)
		if ticker != nil && intervalChanged {
			ticker.Reset(request.config.ScanInterval)
		}
		// Registry metadata and selection settings can change the rendered
		// activity even when the selected IDs stay the same.
		hasEmitted = false
		candidateSet = false
		close(request.done)
	}

	scan := func(forceEmit bool) bool {
		for {
			processes, err := listProcesses(d.lister)
			if err != nil {
				scanFailures++
				d.debugf("process scan failed: %v", err)
				if scanFailures < ScanFailureClearThreshold || !hasEmitted || emitted.None {
					return true
				}
				none := Detection{None: true}
				select {
				case out <- none:
					d.debugf("detection emitted after %d consecutive scan failures: %s", scanFailures, detectionSummary(none))
					emitted = none
					candidate = none
					candidateSet = true
					streak = 0
					return true
				case request := <-d.reconfigure:
					applyReconfigure(request)
					forceEmit = true
					continue
				case <-ctx.Done():
					return false
				}
			}
			if scanFailures > 0 {
				d.debugf("process scan recovered after %d failure(s)", scanFailures)
				scanFailures = 0
			}
			d.debugf("process scan: processes=%d", len(processes))
			current := selector.SelectWithEnricher(processes, processEnricher(d.lister))

			if !candidateSet || !sameDetection(current, candidate) {
				candidate = current
				candidateSet = true
				streak = 1
				d.debugf("detection candidate changed: %s", detectionSummary(current))
			} else {
				streak++
			}

			if !forceEmit && streak < d.config.DebounceCycles {
				d.debugf("detection decision: debounce=%d/%d", streak, d.config.DebounceCycles)
				return true
			}
			if !forceEmit && hasEmitted && sameDetection(candidate, emitted) {
				d.debugf("detection decision: unchanged")
				return true
			}

			select {
			case out <- candidate:
				d.debugf("detection emitted: %s", detectionSummary(candidate))
				emitted = candidate
				hasEmitted = true
				return true
			case request := <-d.reconfigure:
				applyReconfigure(request)
				forceEmit = true
			case <-ctx.Done():
				return false
			}
		}
	}

	if !scan(false) {
		return
	}

	ticker = time.NewTicker(d.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !scan(false) {
				return
			}
		case request := <-d.reconfigure:
			applyReconfigure(request)
			if !scan(true) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func detectionSummary(detection Detection) string {
	if detection.None {
		return "none"
	}
	return "featured=" + detection.Tool.ID
}

func listProcesses(lister ProcessLister) ([]Process, error) {
	if identityLister, ok := lister.(ProcessIdentityLister); ok {
		return identityLister.ListIdentities()
	}
	return lister.List()
}

func processEnricher(lister ProcessLister) ProcessEnricher {
	if factory, ok := lister.(ScanProcessEnricherFactory); ok {
		return factory.NewScanProcessEnricher()
	}
	enricher, _ := lister.(ProcessEnricher)
	return enricher
}

func isNewerInstance(left, right toolCandidate) bool {
	if !left.CreateTime.Equal(right.CreateTime) {
		return left.CreateTime.After(right.CreateTime)
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
