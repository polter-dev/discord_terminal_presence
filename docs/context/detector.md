# detector (package `internal/detector`)

**Purpose:** Scans processes, matches their identity through the registry, evaluates
terminal presence, and selects a featured tool plus other present tools.

**Public surface:** `Process`, `ProcessLister`, identity/enrichment interfaces, `Config`,
`Detector`, `Selector`, `FeaturedTool`, and `Detection` form the scan/selection boundary.
`NewGopsutilLister` is production process input. `ActiveDetectionWithPresence` provides a
one-shot snapshot with real presence eligibility and episode anchors; the plain
`ActiveDetection` free function and `(*Detector).ActiveDetection` method were deleted
(#568): both called `Select` with a nil enricher, so `Process.Owned` was never set from a
real scan and both always returned `Detection{None: true}` against real process data (0 of
1074 real processes came back `Owned=true`). Nothing outside tests referenced either
helper. `EpisodeStore` and its load/save helpers persist elapsed-session anchors.

**Key files:** `internal/detector/detector.go` owns scan/debounce/selection and debug
reporting. `gopsutil.go` preserves structured argv and enriches selected identities;
`gopsutil_identity_darwin.go` lists identities from one retained `kern.proc.all` snapshot,
while `gopsutil_identity_other.go` keeps the gopsutil listing path on Linux and Windows.
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

Darwin identity listing applies the same effective-UID rule early (#594), directly from
each `KinfoProc.Eproc.Ucred.Uid` returned by one
`unix.SysctlKinfoProcSlice("kern.proc.all")`, before any per-process argv or executable
lookup. This is only a prefilter: the authoritative `unixOwnerResolver.Owned` call above
remains in its original enrichment choke point and still fails closed. Processes retained
by the prefilter pay one `CmdlineSlice` and one `Exe` call; `p_comm`, pid, and millisecond
creation time come from the bulk record. Linux and Windows retain the prior gopsutil
listing implementation. The Darwin path deliberately replicates gopsutil v4.26.6's
`Name` rule (a `p_comm` of at least 15 bytes is extended with the basename of argv0), and
its call site flags that a future gopsutil upgrade can drift. Live-host tests re-check
every dropped process with the real owner resolver, compare all identity fields for pids
present in both listings, and hold adversarial long, exactly-15-byte, quoted-argv, and
Unicode/emoji-named helper processes alive across both snapshots. A process whose
effective UID changes from foreign to owned during one scan can be dropped one scan
earlier than under the old per-process reads; this follows the existing fail-closed
direction and self-heals on the next scan.

Ownership is resolved unconditionally in `enrich` (never skipped, never reordered after
TTY), but once it is known to be `false` the enricher now returns immediately (#566):
before this, TTY resolution, the tmux query, and the atime stat always ran for every
candidate even though the ownership gate discards the result. On Unix that meant stat-ing
another user's TTY device per foreign candidate per scan; on Windows
`windowsTTYResolver.Resolve` spawns a child `termp.exe` per foreign matched PID per scan.
`process.Name`, `Argv0`, and the flattened `Cmdline` are bounded to `maxIdentityFieldBytes`
(4 KiB, rune-safe) in `processIdentity` before they ever reach `registry.MatchProcess`
(#565): matching only ever inspects identity plus the immediate subcommand, so an
attacker-controlled multi-MiB `argv` (measured at ~360ns/byte in the matcher) can no longer
push a scan past the interval. The structured `Argv` slice is left unbounded since matching
only reads its first couple of elements.

PID reuse between identity capture (`ListIdentities`), enrichment (`Enrich`), and ownership
(`OwnerResolver.Owned`) is mitigated, not eliminated (#569, plausible-not-confirmed): each
is an independent `psprocess.NewProcess(pid)` lookup, so a foreign process that exits and
whose pid is immediately recycled to one of the user's own tools could otherwise mix a
foreign identity (name/argv) with the new process's cwd and ownership. `processIdentity`
now also captures `CreateTime`; `OwnerResolver.Owned(pid, createTime)` takes that captured
value and fails closed on a mismatch, and `gopsutil.go`'s `enrichVerifyingInstance` discards
freshly read enrichment fields (keeping the original identity) rather than merging in data
read from a different process instance. A zero `createTime` skips the check rather than
failing (used when identity capture itself couldn't read it).

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
fail open instead of treating a silent zero as inactivity. Aggregate recent
activity includes available per-process deltas and skips unavailable siblings
(#517, #526); when every sample is unavailable, aggregate activity is zero.
Recovery establishes a fresh per-process baseline. Process observations are keyed
by PID and creation time, so starts, exits, and PID reuse cannot mix unrelated
cumulative CPU totals.

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

`LoadEpisodeStore` bounds the file the same way `internal/usage.Load` does (#567):
`maxEpisodeStateFileSize` (1 MiB, `io.LimitReader`) and `maxEpisodeEntries` (1024, dropping
the least recently active episodes first by `LastAtime`/`PresentSince` on load only, since
runtime `Observe` growth is already bounded by the real running-process count). A missing
file returns an empty store with a nil error; a corrupt, oversize, or otherwise unreadable
file also returns an empty store but with a non-nil error now (previously nil, identical to
absent) so a caller can tell "nothing here yet" from "the real file is broken." `run()`
uses that distinction: a non-nil load error disables `saveEpisodes` for the entire run so
the empty in-memory store is never saved back over a corrupt file. `internal/usage.Load`
got the matching fix: a JSON-corrupt file now returns its unmarshal error instead of a nil
error, and `cmd/termp/main.go`'s daemon startup (`usagepkg.Load` call site, `saveUsage`)
disables `usage.Save` for the run when the load failed.

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

`selectConsolePeer` (`tty_windows_logic.go`) was deleted (#570): it had no production
caller, only its own `_test.go`, and staticcheck's `unused` check counts test usage as a
live reference, so the orphaned helper was never flagged despite the "selection helpers
must have live callers" rule above. Its apparent intent (picking a console-sharing peer
PID) could not be confidently established or safely wired into `inspectWindowsConsole`
without Windows hardware to verify against, so it was removed along with its test rather
than guessed into production.
