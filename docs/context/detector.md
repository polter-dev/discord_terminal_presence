# detector (package `internal/detector`)

**Purpose:** Scans processes, matches their identity through the registry, evaluates
terminal presence, and selects a featured tool plus other present tools.

**Public surface:** `Process`, `ProcessLister`, identity/enrichment interfaces, `Config`,
`Detector`, `Selector`, `FeaturedTool`, and `Detection` form the scan/selection boundary.
`NewGopsutilLister` is production process input. `ActiveDetection*` provide snapshots.
`EpisodeStore` and its load/save helpers persist elapsed-session anchors.

**Key files:** `internal/detector/detector.go` owns scan/debounce/selection and debug
reporting. `gopsutil.go` preserves structured argv and enriches selected identities.
`tty_*.go` and `idle_cpu_*.go` own platform presence. `episode.go` owns persistence.

**Invariants / gotchas:** Registry matching is restricted to process identity:
name/executable/argv0 and recognized runtime entrypoints. Catalog regexes never inspect
later arguments; exclusions see identity plus only the immediate subcommand.

Presence and featured eligibility differ on Windows. Losing foreground starts
the terminal idle clock; the window's last foreground time is retained across
scans, and `idle_clear_timeout` controls the resulting grace period. While the
window is focused, recent process CPU activity is sufficient for featured
eligibility even without recent system input. While it is unfocused, CPU
activity only corroborates the retained focus clock and cannot extend the
grace period. A matched process that is still attached to a resolved terminal
remains eligible for `Others`, however, because an unfocused terminal window
is still a legitimate member of the running collection. Processes with a
definitive lack of a terminal and detached tmux processes are excluded from
both sets.

On macOS and Linux, collection membership continues to use the same terminal
activity eligibility as featured selection. When a resolved terminal has no
trustworthy atime (including Linux devpts mounted with the usual `relatime`),
recent per-process CPU activity is used as the idle signal. CPU observations
carry sampling availability separately from the sampled total; failed samples
fail open instead of treating a silent zero as inactivity.

When one tool has multiple eligible processes, the selector keeps the current
representative unless the same challenger has a clear per-process CPU or recent
terminal-activity advantage for two consecutive scans. With no distinguishing
activity signal, the newer process remains the fallback. This instance-level
hysteresis prevents alternating sessions from changing the displayed directory
and elapsed-session anchor every scan.

Per-tool enablement is applied before matched processes are enriched or selected. A
disabled tool cannot become featured or appear in `Others`; changing enablement
reconfigures the running detector and immediately re-resolves the selection while
preserving eligible tools' directories and episode anchors. Selection helpers must have
live callers; the CI Staticcheck job treats orphaned helpers as errors.

Process-list failures retain the last presence for one or two consecutive
scans. On the third consecutive failure, the detector emits `None` immediately
so the writer clears stale Discord presence. A successful scan resets the
failure counter, and normal detection debounce applies when presence recovers.
Blocked detection emissions continue to service reconfiguration requests. A
mid-emit reload invalidates the candidate derived from the old registry/config,
acknowledges the reload, and immediately re-scans before emitting.

Gopsutil `CmdlineSlice` is preserved as structured argv; string cmdline remains a
fallback. Episode-store load and save failures reach the detector debug callback. The
detector continues with its in-memory store, preserving scan and elapsed-timer semantics.
`RunReadOnly` loads and consumes the episode store without incremental or shutdown saves;
live CLI watch uses it so only the daemon persists `presence.json`.

**Depends on / used by:** Depends on `internal/registry` and gopsutil; produces snapshots
for the daemon, status, presence mapping, watch, and usage recording.

**Open questions / TODO:** Windows classic-console focus and idle detection is implemented
and covered by dedicated Windows tests. Windows Terminal/ConPTY cannot be attributed to an
active tab with generic Win32 APIs, so resolution deliberately fails open there. Five
generic selector tests remain skipped on Windows because their TTY-atime/tmux fixtures
model Unix semantics, not because Windows presence is unimplemented; neither #275 nor
#304 tracks a residual detector gap.
