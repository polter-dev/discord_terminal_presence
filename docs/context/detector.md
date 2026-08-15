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

Every matched candidate must be affirmatively proven to belong to the current effective
user before it can be considered for either the featured tool or the `Others` collection
(#450: a shared-host process with a resolved controlling terminal previously had no
ownership check at all and could be featured on another user's Discord profile). `Process`
carries an `Owned bool` that defaults to `false`; `presenceProcessEnricher.enrich`
(`tty.go`) resolves it via an `OwnerResolver` (`unixOwnerResolver` compares gopsutil's
effective UID via `Uids()` to `os.Geteuid()`; `windowsOwnerResolver` compares process
token-owner SIDs via `OpenProcessToken`/`GetTokenUser`, since gopsutil does not implement
`Uids()` on Windows) independently of TTY resolution, so it still gates a process with no
terminal information at all. `SelectWithEnricher` checks `!proc.Owned` immediately after
enrichment and before either `presenceEligible` or `inactiveCollectionEligible` run — this
is the single choke point both selection paths pass through, so a future third selection
path inherits the filter by construction rather than needing its own check. Any resolver
error (permission denied, pid exited mid-scan, platform unsupported) fails closed: the
process stays excluded rather than defaulting to included. This is the opposite default
from `TTYState`'s documented "unknown fails open" — deliberate, since ownership is a
security boundary rather than a presence/UX signal, and losing a detection is preferable
to leaking a foreign user's tool identity and elapsed timer onto the caller's Discord.
Verified: Unix path (effective-UID comparison, fail-closed-on-lookup-failure, and the
Selector-level exclusion/inclusion contract) has real unit coverage exercised on this
machine (`owner_unix_test.go`, `detector_test.go`). The Windows token-SID path
cross-compiles and vets (`GOOS=windows go build|vet`) but has **not** been executed on
Windows hardware; treat it as implemented-but-unverified until a Windows run confirms it.
The original cross-user leak was reasoned from the code path, not reproduced with a real
second OS user account (no multi-user box was available); the reproduction that *was*
done is a direct unit-level demonstration that pre-fix `SelectWithEnricher` accepted and
even featured a `Process{Owned: false}` candidate over an owned one (see
`TestSelectorExcludesForeignOwnedProcess` / `TestSelectorExcludesProcessWhenOwnerLookupFails`,
confirmed failing against the pre-fix selector before the gate was added).

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

Windows classic-console inspection runs `AttachConsole` in a short-lived child so it
cannot disturb the daemon's console handlers. That probe uses `CREATE_NO_WINDOW` and the
absolute `termp.exe` sibling of the running process image; it must never invoke a Scoop
shim, because the shim can allocate a visible console for the real probe it launches
(#508). Config and state-file writes do not themselves spawn a child; their activity can
cause a rescan, which is why the probe window appeared correlated with saves.

Windows ConPTY fail-open covers hidden console windows too (#501). `inspectWindowsConsole`
(`tty_windows.go`) resolves a classic terminal only when `GetConsoleWindow` returns a real,
*visible* top-level window; the decision is factored into the pure, host-testable
`shouldConsoleFailOpen`/`windowsConsoleWindow` (`tty_windows_logic.go`, tested by
`TestShouldConsoleFailOpen`). Fail-open (`conPTY=true`, so `Resolve` stays `TTYUnknown` and the
tool is always featured) now fires for `hwnd == 0` (the original classic-ConPTY path), for a
non-existent handle, and for a non-visible handle. The last case is the bug: on some Windows
builds a headless ConPTY conhost (`OpenPseudoConsole`/`conhost.exe --headless`) returns a
non-null *hidden* HWND; resolving it froze the activity clock (the foreground
`WindowsTerminal.exe` window never shares that hidden window's `GA_ROOTOWNER`), so idle-clear
dropped actively-used sessions after `idle_clear_timeout`. A real terminal window carries
`WS_VISIBLE` even when minimized, so classic-console focus/idle detection is unaffected.
Runtime confirmation is deferred to the Windows tester (no Windows hardware here); the change
cross-compiles and vets under `GOOS=windows` and the host tests are green.

**Open questions / TODO:** Windows classic-console focus and idle detection is implemented
and covered by dedicated Windows tests. Windows Terminal/ConPTY cannot be attributed to an
active tab with generic Win32 APIs, so resolution deliberately fails open there. Five
generic selector tests remain skipped on Windows because their TTY-atime/tmux fixtures
model Unix semantics, not because Windows presence is unimplemented; neither #275 nor
#304 tracks a residual detector gap.
