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
activity eligibility as featured selection.

Process-list failures retain the last presence for one or two consecutive
scans. On the third consecutive failure, the detector emits `None` immediately
so the writer clears stale Discord presence. A successful scan resets the
failure counter, and normal detection debounce applies when presence recovers.

Gopsutil `CmdlineSlice` is preserved as structured argv; string cmdline remains a
fallback. Episode-store load and save failures reach the detector debug callback. The
detector continues with its in-memory store, preserving scan and elapsed-timer semantics.

**Depends on / used by:** Depends on `internal/registry` and gopsutil; produces snapshots
for the daemon, status, presence mapping, watch, and usage recording.

**Open questions / TODO:** Windows tty-presence coverage remains tracked in #183.
