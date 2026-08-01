# config (package `internal/config`)

**Purpose:** Defines termp's user configuration, defaults, validation, path migration,
serialization, and hot-reload manager.

**Public surface:** `Config` and its nested UI/display/privacy/CTA/tool types are the
runtime schema. `Default`, `DefaultPath`, horizon-protected settled `Load`/`LoadPath`,
settled `LoadReadOnly`/`LoadPathReadOnly`, explicitly unprotected
`LoadUnsettled`/`LoadPathUnsettled`, and `Save` resolve and persist it.
`AnnotatedSample` and `InitFile(path, force)` support `termp config init`. `Manager`
watches a path and publishes validated changes. `ValidateFeedbackURL` and
`ValidateDurationField(field, value)` expose individual field validators so callers
(the settings TUI) can reject a bad value at the point of entry using the exact rules
`Load`/`Save` enforce, rather than re-implementing parsing.

**Key files:** `internal/config/config.go` contains the schema, validation, platform-aware
paths, annotated sample, initialization, load, and atomic save. `internal/config/manager.go`
contains polling-based reload and config-directory setup.

**Invariants / gotchas:** A missing config loads defaults; invalid values return errors
or documented warnings rather than silently changing privacy behavior. An existing
config that cannot be read, decoded, or validated returns an in-memory default config
with global presence disabled; a missing file still returns enabled first-run defaults.
The manager keeps that fail-closed startup config until a valid hot reload replaces it,
without persisting a last-good copy. Its buffered reload-result stream coalesces bursts
to the newest ordered success or failure, so consumers cannot clear a newer failure with
an older queued success. Watcher-backend errors use a separate coalescing channel, so
they cannot replace an unread config reload, change `LastError`, or invalidate the
last-good config; daemon/watch rendering labels them as watcher errors instead of reload
failures. Windows migrates legacy state to the native config directory on a best-effort
basis.

All default config entry points defend against non-atomic saves, including
truncate-then-write and unlink-then-recreate. Those saves can expose a transient missing,
empty, or partial file; an empty or partial file is often still syntactically valid TOML
on its own. Before accepting a read, `settledConfigSnapshot` normally waits for two
consecutive reads of the file to agree, reading every ~15ms, up to 20 attempts (~300ms
budget). A candidate is instead provisional when a previously accepted file is now
missing, when it is an existing empty file, when its bytes are a strict prefix of the
manager's last successfully accepted, error-free file snapshot, or (since #462) when its
bytes do not parse as TOML at all. Provisional candidates must remain unchanged across
the full settle budget before acceptance.

The undecodable-bytes rule closes #462. The prefix rule only recognises a writer that
re-emits the accepted bytes verbatim before appending. A writer that also edits an
earlier line — the ordinary case, an editor saving a changed document — diverges from the
accepted content on its first chunk, so every later mid-write read is a non-prefix and was
accepted as settled the moment two 15ms polls happened to straddle one inter-chunk pause.
`Manager.Reload` then handed a file truncated mid-string-value to the decoder and returned
`toml: line 2 (last key "idle_clear_timeout"): unexpected EOF; expected '"'` to the
caller, which the settled-read model forbids. This was **reproduced deterministically**
(accept `scan_interval = "9s"\nenabled = false\n`, truncate, write
`scan_interval = "5s"\nidle_clear_timeout = "`, stall 60ms, then finish): 5/5 failures
before the fix, and the mutation check — forcing the new branch to `false` — fails the
suite. It is a real defect, not a flaky assertion; the randomized schedule test was merely
the (rare, timing-dependent) way CI noticed.

The probe is `parsesAsTOML`, decoding into a generic `map[string]any` so only *syntax*
counts as evidence of a write in flight. Unknown keys, wrong-typed values, and failed
semantic validation are real config errors and still surface normally. A config a user
genuinely saved unparseable is therefore stable, holds for the full ~300ms budget, and
then surfaces its parse error — inside the settle budget, never the 3s horizon, and never
an unbounded wait. `settledConfigSnapshotUntilWith` memoises the provisional verdict per
distinct candidate so the probe runs once per candidate, not once per poll.

Both `parsesAsTOML` and the real decode in `loadSnapshotWithMetadata` run
`tomlNestingTooDeep` first and never call `toml.Decode` on bytes that fail it. This closes
#497: BurntSushi/toml v1.6.0 decodes deeply-nested inline tables/arrays in O(n^2), so a
syntactically valid `x = {a={a=...N...}}` document nested ~15000 levels deep (~58KB, well
under the 1 MiB `maxConfigFileSize` cap) took ~18s to decode with no deadline — hanging the
`parsesAsTOML` probe, `LoadPath`/`LoadPathReadOnly`, and the live daemon's `Manager.Reload`
alike. `tomlNestingTooDeep` is an O(n) byte scan of the raw config bytes that tracks the
current bracket-nesting depth of `{`/`[` while a small state machine skips brackets found
inside basic (`"..."`, with backslash escapes), literal (`'...'`), multiline-basic
(`"""..."""`), and multiline-literal (`'''...'''`) strings, and inside `#` comments. Any
document whose depth exceeds `maxTOMLNestingDepth` (100 — real configs nest 1-2 levels)
fails closed with `errConfigNestingTooDeep` ("config nesting too deep") the same way any
other invalid config does (consistent with #462's fail-closed handling), without the byte
scan itself ever taking more than linear time regardless of how deep the (rejected) nesting
claims to be.

If a provisional candidate changes during that budget, `Manager.Reload` leaves last-good
and `LastError` untouched and relies on the save's completion to fire another fsnotify
event. Standalone loads and manager construction have no last-good value to retain, so
they retry only within a named 500ms standalone settle bound. At the bound,
`LoadReadOnly`/`LoadPathReadOnly` and manager construction carry on with the newest
snapshot, while destructive `Load`/`LoadPath` return the distinguishable
`ErrConfigBeingWritten` ("config is being written right now; try again") so a
whole-document editor cannot overwrite the file from an unsettled guess. A deliberate
deletion, blanking, or trailing-line deletion still loads after remaining stable for the
full ordinary settle budget, preserving reset and shortening paths. A missing file is
not provisional when there is no previously accepted file and returns immediately,
keeping first run fast. That intentional first-run exception means a standalone load
cannot distinguish a genuinely absent file from an unlink/recreate window without prior
state.

`LoadUnsettled` and `LoadPathUnsettled` are the only exported single-read exceptions. The
name makes the protection opt-out visible in review; no production caller currently uses
either. The unexported `snapshotConfigFile` is the raw primitive used by the settle
algorithm and those explicit exceptions.

Every successfully decoded reload reaches one state-commit choke point,
`Manager.acceptReloadLocked`. Until #447, the horizon guard there covered only the global
top-level `enabled` key, so a truncating writer that stalled could silently revert a
directory allowlist, a per-tool `show_directory`/`directory_basename_only` override, or a
per-tool opt-out after only the ordinary ~300ms settle budget rather than the three-second
horizon. The choke point now also gates on `permissivenessLoosened`, which resolves both
the current and candidate `Config` through `Config.Resolve` (the same path presence
mapping uses) for every tool ID present in either config plus the tool-agnostic global
posture, and compares a small `privacyPosture` (enabled, show-directory, basename-only,
whether the allowlist restricts anything) rather than a hand-written list of field names.
Two tests (`TestPrivacyPostureCoversAllPrivacyFields`,
`TestPrivacyPostureCoversToolOverridePrivacyFields` in `config_test.go`) use reflection
over `Privacy` and `ToolOverride` to fail the day a new field is added without a conscious
decision about whether the posture snapshot needs to grow — the actual defect this bug
family (#410 → #425 → #434 → #435 → #438 → #440 → #447) kept regenerating.

An `enabled` transition from `false` to `true` still applies immediately when TOML
metadata says the file explicitly defined the top-level key (`enabledDefined`); when the
global `enabled` flag itself changes, `permissivenessLoosened` skips its own enabled-only
dimension, but **only for the tool-agnostic global posture** (tool ID `""`), so it cannot
re-gate that already-vetted, explicit transition merely because the global posture's own
resolved-enabled state flips. This neutralization is deliberately *not* applied to any
real per-tool ID's enabled dimension.

That distinction matters because of a second, independent subtlety in `Config.Resolve`:
it returns early when the global flag is off, *before applying any per-tool override at
all*. If prev were resolved at its real, disabled value, a per-tool tightening (e.g. an
explicit `[tools.vim] enabled = false`) would never show up in prev's posture in the first
place — and neutralizing the enabled dimension on top of that would have blinded the guard
completely for the disabled→enabled transition specifically, which is exactly the shape of
a top-down non-atomic writer stalling after line 1 of a file that starts with `enabled =
true`. An independent reviewer found and reproduced this in round 3, at 16ms instead of the
3s horizon. `permissivenessLoosened` now resolves **prev** with `Enabled` forced to `true`
before computing its posture (a local copy used only for this comparison, not the real
`prev.Enabled` used anywhere else), so per-tool overrides are actually applied and can be
compared against next's resolved state; only the global (`""`) posture's own enabled
dimension is then neutralized.

A `[tools]` override key must be non-blank. `""` is the sentinel that posture comparison
uses for the tool-agnostic global row, and `""` is also a legal TOML map key, so a
`[tools.""]` section occupies the same slot as the sentinel. That was **a demonstrated
guard bypass, not a theoretical one**: with a `[tools.""]` override that itself equalizes
the dimension being dropped (`directory_allowlist = []`, the documented per-tool opt-out
form), the `""` row reads unrestricted in both the previous and the next config, so the
global allowlist can be truncated away with nothing left to proxy for the unoverridden
real tools. Reproduced against the pre-fix code through the real manager, with the
`[tools.""]` section ordered before `[privacy]` so a top-down truncation keeps it:

    WITH [tools.""]        AFTER (321ms) DirectoryAllowed(/home/secret)=true   <- leaked
    CONTROL (no tools."")  AFTER (324ms) DirectoryAllowed(/home/secret)=false  <- gated

An earlier attempt to reproduce this used a `[tools.""]` override that left the allowlist
dimension untouched; that keeps prev's `""` row restricted, so the gate fires and the leak
is hidden. The equalizing override is the necessary ingredient — worth knowing before
concluding a sentinel collision is unreachable.

`validate` now rejects blank/whitespace override IDs, which closes it by construction
rather than asking the guard to tolerate the collision, and matches the blank-ID rejection
custom tools already have. Verified on the fixed code: the same config fails closed at load
(`tools: override id must not be blank`) and the truncation is gated. `AnnotatedSample`
emits no `[tools]` sections at all, so unlike the `directory_allowlist = []` case there is
no upgrade exposure.
`TestManagerReloadPreservesPerToolOptOutWhileGlobalEnabledAlsoLoosens` is the regression:
a config with the global flag off and `[tools.vim] enabled = false` moves to a stalled
partial containing only `enabled = true` (the per-tool override not yet rewritten), and
vim must stay resolved-disabled until the horizon elapses even though the global flag
itself is allowed to flip immediately.

With that fixed, every *other* privacy dimension — including a per-tool ID's own enabled
dimension — is genuinely compared normally even when the global `enabled` changes at the
same time, so a simultaneous, unrelated loosening (e.g. an explicit `enabled = true`
arriving in the same edit that also happens to truncate away an allowlist, or drops a
per-tool opt-out) still pays the horizon. Beyond the one global-posture exemption above,
this fix does **not** implement a full explicit-vs-defaulted distinction for every field:
unlike `enabled`, an explicit, deliberate loosening of `show_directory`,
`directory_basename_only`, the global allowlist, or a per-tool override still pays the same
three-second horizon as an ambiguous one. This is a disclosed, conservative trade-off
(latency, not a privacy leak) rather than a silent gap; for `show_directory`/
`directory_basename_only`, it is actually unreachable in practice, since their permissive
values can only ever come from bytes explicitly present in the file (see below), never
from an absent key.

The candidate snapshot is recorded at the first sampled reload and must match the
snapshot seen at later reload samples, including the retry fired for the separate
three-second loosening horizon (#434). While a loosening is pending, the manager retains
its current and accepted snapshots, leaves `LastError` unchanged, and publishes no reload
result; daemon behavior and status therefore continue to reflect the active last-good
config. A retry makes a deliberate blank, deletion, or trailing-line deletion take effect
after the horizon even if no further filesystem event arrives. If a retry observes a
different loosening snapshot, it starts a fresh horizon and arms another retry without
relying on an fsnotify event. Transitions to `false`, non-loosening changes, and explicit
`enabled = true` (absent any other simultaneous loosening) keep the normal settle latency.
Reload attempts are serialized so concurrent fsnotify events cannot commit stale
candidates around the guard.

Because `Default().Privacy.ShowDirectory` is `false` and `Default().Privacy.DirectoryBasenameOnly`
is `true` — both already the most restrictive value — a *global* loosening on either field
(`false`→`true` or `true`→`false` respectively) can only be produced by bytes explicitly
present in the surviving read; an absent key always resolves to the restrictive default.
The ambiguous, truncation-vulnerable case for these two fields is therefore specifically a
*per-tool* override (a `*bool` pointer) going from explicit-and-restrictive to nil/absent,
falling back to a more permissive global or built-in value — exactly what
`permissivenessLoosened`'s per-tool resolution catches. The global directory allowlist
reaches an empty, unrestricted resolved value from either an absent key or an explicit,
present-but-empty `directory_allowlist = []` (#449 warns on the latter rather than
rejecting it — see below); either way `permissivenessLoosened` sees the same resolved
`allowlistRestricted: false` and gates the transition the same way, since it operates on
the resolved posture from `Config.Resolve`, entirely independent of which source form
`validate` accepted. `TestManagerReloadStillGatesGlobalAllowlistBecomingPresentButEmpty`
is a regression for this: it confirms the #449 warning downgrade (below) did not
accidentally let a global allowlist loosening skip the horizon. A per-tool allowlist
override carries its own explicit-vs-absent tracking already (`allowlistSet`, set from
`meta.IsDefined("tools", id, "directory_allowlist")`), and an explicit, present-but-empty
per-tool override remains a valid, deliberate way to opt that tool out of a restrictive
global allowlist (see the `directory_allowlist` section below).

`privacyPosture`'s `allowlistRestricted` is a boolean (whether the resolved allowlist has
any entries at all), not a comparison of the entries themselves. It does not notice
entry-level widening: `["/a/b"]` → `["/a"]` is a broader allowlist but still reads as
"restricted", and a disjoint swap (`["/a"]` → `["/b"]`) is invisible the same way. This is
a known, accepted limit rather than an oversight: no strict byte-prefix of a TOML array
parses as valid TOML, so a truncating, stalling writer cannot produce a widened-but-valid
allowlist the way it can drop an allowlist to empty or drop a per-tool override to absent —
only a complete, deliberate rewrite can change specific entries, and the guard is not
required to delay a config that was never truncated in the first place. If a future
allowlist representation ever became reachable through a partial write (or a caller started
constructing `Config` values from something other than a fully-decoded TOML document), this
reasoning would need to be revisited.

`Manager` currently has no lifecycle/`Close` method. Its `time.AfterFunc` retry retains
the manager until it fires and may read the config path after the daemon has otherwise
shut down or an isolated path has been removed. Reload tolerates a missing file, so this
is harmless today; any future manager lifecycle must stop the pending retry.

The guard is deliberately time-bounded, not an absolute guarantee. A writer that stalls
longer than the three-second horizon while the file is a valid partial omitting
`enabled` can still revert the opt-out. That residual is inherent because the stalled
snapshot is byte-and-time indistinguishable from a deliberate blank, which must
eventually restore defaults.

The **daemon and interactive-watch** entry points close the formerly eventless startup
gap (#435) by installing the watcher before an explicit settled `Manager.Reload`, then
using only the post-reload `Current` config; a completion event during that sequence is
therefore queued instead of missed. `NewManagerPath` now also starts from a settled
snapshot. If that snapshot is an existing empty file, construction cannot distinguish an
in-flight truncation from a deliberate blank, so it seeds fail-closed with presence off,
retains the blank accepted baseline, and routes the defaulted candidate through the same
`enabled`-loosening choke point. A completed explicit opt-out then applies immediately;
a genuinely blank file restores defaults after the existing horizon. Normal non-empty
configs whose `enabled` key is absent seed enabled defaults after one settle interval,
and missing first-run files seed them immediately.

`Load`/`LoadPath` are safe by default for callers that may save the loaded whole document
back over the user's file (#438). If an existing file is still empty or whitespace-only
after the normal settle budget, these entry points treat it as ambiguous for the same
three-second loosening horizon used by `Manager`: content that appears within the
horizon is settled and returned, while a blank that persists for the entire horizon is
accepted as a deliberate reset. Normal nonblank loads still take only the ordinary
settle interval. A file that changes continuously instead returns
`ErrConfigBeingWritten` after the separate 500ms standalone bound. Setup and settings
propagate that error before installing any save callback or entering their TUI, leaving
the on-disk bytes untouched. Explicitly read-only CLI paths use
`LoadReadOnly`/`LoadPathReadOnly`, so they inherit the normal settle protection without
paying the update horizon and render from the newest observed snapshot after the bound.

Until #448, `settledConfigSnapshotForLoadWith`'s horizon loop could exit early in two
ways: `boundedSettledConfigSnapshotWith` always passed an empty `accepted` snapshot, so a
file that disappeared mid-horizon hit the same missing-and-no-history fast path used for a
genuine first run and returned defaults immediately (route A); and the loop left as soon
as it saw one non-blank read and returned whatever re-settled, without re-checking whether
that re-settled result was itself still ambiguous (route B, a single-poll content flicker
during a stalled truncation). Both routes reproduced the exact #438 harm the horizon
exists to prevent, ~2.3s early. `boundedSettledConfigSnapshotWith` now takes an explicit
`knownAccepted fileSnapshot` parameter: the horizon loop's first read still passes an
empty snapshot (preserving the fast, first-run-safe path), but every re-settle attempt
after an ambiguous-blank read has been observed passes `fileSnapshot{exists: true}`, so a
subsequent disappearance is treated as provisional and must hold missing for a full settle
budget, not just look missing once. On leaving the horizon loop, the result is also
re-checked: if it is still ambiguous-blank, or missing after the file was known to exist,
the loop continues instead of returning. `TestLoadPathHorizonSurvivesFileDeletionMidHorizon`
(route A) and `TestLoadPathHorizonSurvivesContentFlickerMidHorizon` (route B) exercise this
with an injected snapshot function and a virtual clock rather than real timing, per #441's
own lesson about clock-driven flicker tests being flaky in CI.

A candidate that becomes provisional-stable partway through the normal budget also
cannot reach acceptance in that call: reload is a no-op, while standalone loads retry
with the new content as their first snapshot. With no accepted baseline, a stable
non-empty partial cannot be recognized as a strict prefix; if it parses it receives the
normal two-read settle check, and if it does not it is provisional on the #462 rule.

#462's regression set is `TestManagerReloadWaitsOutTornMidStringWrite` (the deterministic
torn-write repro; the pre-existing `TestManagerReloadRandomWriterSchedules` only reaches
that window by timing luck and is kept as-is, not loosened),
`TestManagerReloadStillReportsGenuineSyntaxError` and
`TestLoadPathStillReportsGenuineSyntaxError` (a truly broken config must still error, and
`LoadPath` must return the parse error rather than `ErrConfigBeingWritten` — the ~300ms
settle fits inside the 500ms standalone bound), and `TestGeneratedConfigIsNotProvisional`,
which asserts against the document `Save(Default())` actually emits so a future bytes-level
rule cannot classify every shipped user's config as an in-flight write.

`ResolvedTool.DirectoryAllowed` applies the effective directory privacy policy but does
not format paths for display. Display reduction belongs to the presence mapping boundary,
so config does not expose a second directory formatter that could diverge from it.
Authored allowlist entries remain in their original form in the loaded `Config`; `~`
expansion and path cleaning happen lazily inside `DirectoryAllowed`. Consequently a
whole-document save preserves portable entries such as `~/projects` instead of baking
the current home directory into the user's config (#479). This applies to both global
and per-tool allowlists. Tests that redirect home while exercising this expansion set
both `HOME` and `USERPROFILE`, matching `os.UserHomeDir` on Unix and Windows respectively.
`permissivenessLoosened` remains unchanged: it still compares
the same resolved privacy posture dimensions through `Config.Resolve`, while the actual
path expansion is deferred to the point where a candidate directory is checked.
`DirectoryAllowed` treats a zero-length `DirectoryAllowlist` as "no restriction configured"
(allow every directory once `show_directory` is on) — this is intentional for a genuinely
absent key, but before #449, validation-time path expansion silently dropped
blank/whitespace-only entries, so a user-authored `directory_allowlist = [""]` (or any
list whose every entry expanded to nothing) silently collapsed to the same zero-length,
allow-everything slice, and `Save`
then cemented it on disk as `[]`. `validate` now rejects any blank/whitespace-only
allowlist entry, at both the top-level `[privacy]` allowlist and every per-tool override,
with a validation error (consistent with #419's reject-don't-silently-strip approach) —
the entry is never reached by `expandPaths`; no generated config has ever contained a
blank entry, so this is always a typo.

A **top-level** `directory_allowlist` that is present but has zero entries
(`directory_allowlist = []`, tracked via `meta.IsDefined("privacy", "directory_allowlist")`)
is a different case, and the first cut of this fix got it wrong: it initially rejected this
too, on the reasoning that there is no legitimate reason to write `[]` explicitly instead of
omitting the key. Lead review caught that `termp config init`'s `AnnotatedSample` had always
emitted exactly that key, so the hard rejection would have silently disabled presence (the
config fails to load, and per #395 the daemon starts with presence off) for every user who
had ever run `config init` — the exact silent-failure shape this whole issue family exists
to eliminate, just relocated. A present-but-empty top-level allowlist therefore now loads
successfully, still resolves to "no restriction configured" (identical to an absent key),
and appends a `Config.Warnings` entry noting it allows every directory and can be removed;
`Warnings` is already surfaced at startup and in `status`. `AnnotatedSample`'s
`directory_allowlist` line is commented out so a **new** `config init` does not generate a
config that immediately warns; existing on-disk configs keep the active line and now load
with a warning instead of failing.

A **per-tool** override with zero entries is not warned on at all: `docs/product/config-schema.md`
already documents an explicit, present-but-empty per-tool `directory_allowlist` as the way
to opt that one tool out of a restrictive global allowlist, and `Config.Resolve` implements
exactly that via `allowlistSet` — there is nothing ambiguous to flag there. `Save` preserves
that explicit empty override as a non-nil empty slice, so the TOML encoder emits the key and
the opt-out survives a save/reload round trip; tools without an override continue to inherit
the global allowlist.

Discord-facing config is rejected during load when it cannot produce a valid activity.
Tool and custom-tool buttons allow at most two entries; labels are non-empty and at most
32 characters, and URLs are absolute HTTP(S) URLs. Details/fallback text and custom-tool
identity, display, and resolved image fields are bounded before registry construction;
custom-tool display names must contain 2–128 characters.
The feedback target is likewise bounded and restricted to an absolute HTTP(S) URL.
Config reads are capped at 1 MiB before TOML decoding.

The three duration-typed fields (`scan_interval`, `idle_clear_timeout`,
`headliner_idle_timeout`) share one zero-value policy table, `durationFieldsAllowZero`
(only `idle_clear_timeout` permits zero). Both load/save `validate` and the exported
`ValidateDurationField` consult it, so the settings TUI cannot accept a value the next
load would reject (#475); a bad value like `"5"` (no unit), `"0"`, `"fast"`, or
`"2 minutes"` fails identically at entry and at load.

Most watch tests write config changes atomically so they exercise malformed content
rather than a truncation window. Dedicated regression tests use deliberately divergent,
chunked, shrinking, unlink/recreate, and rename/append writers. The destructive-load
regression sweeps truncate stalls of 50ms, 250ms, 400ms, and 1s across both sides of the
normal settle budget, then asserts the saved TOML still contains `enabled = false` and
the user's `pin`. A fixed-seed randomized writer-schedule property test asserts that a
save whose final content sets `enabled = false` never exposes `enabled = true` before
completion. Schedules stalled beyond the loosening horizon are intentionally excluded
as the documented residual. A deterministic snapshot-reader and virtual-clock seam
structurally supplies different valid bytes on every settle read; it asserts read-only
and destructive standalone loads return within the bound, with the read-only path
decoding the newest snapshot and the destructive path producing
`ErrConfigBeingWritten`. Command-level coverage asserts setup and settings leave the
file byte-identical when they receive that error.

`InitFile` uses `Lstat` and refuses symlinks and every other non-regular destination even
with `force`. It writes a temporary file in the destination directory and atomically
renames it. New files are created `0600`; forced replacement of an existing regular file
preserves that file's permission bits. Migration copies also preserve the source mode.
Without `force`, an existing regular file is not replaced.

**Depends on / used by:** Uses BurntSushi TOML and the standard library. The CLI,
detector, presence mapping, TUI, registry construction, and update policy consume it.

**Open questions / TODO:** None currently.
