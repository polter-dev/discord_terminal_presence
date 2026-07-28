# config (package `internal/config`)

**Purpose:** Defines termp's user configuration, defaults, validation, path migration,
serialization, and hot-reload manager.

**Public surface:** `Config` and its nested UI/display/privacy/CTA/tool types are the
runtime schema. `Default`, `DefaultPath`, horizon-protected settled `Load`/`LoadPath`,
settled `LoadReadOnly`/`LoadPathReadOnly`, explicitly unprotected
`LoadUnsettled`/`LoadPathUnsettled`, and `Save` resolve and persist it.
`AnnotatedSample` and `InitFile(path, force)` support `termp config init`. `Manager`
watches a path and publishes validated changes.

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
missing, when it is an existing empty file, or when its bytes are a strict prefix of the
manager's last successfully accepted, error-free file snapshot. Provisional candidates
must remain unchanged across the full settle budget before acceptance.

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

Every successfully decoded reload reaches one state-commit choke point. At that boundary,
the guard applies specifically to the global top-level `enabled` key; it does not cover
other default-true settings such as update checks or display/CTA options. An `enabled`
transition from `false` to `true` applies immediately when TOML metadata says the file
explicitly defined the top-level key. When the value came from the default because the
key is absent, the candidate snapshot is recorded at the first sampled reload and must
match the snapshot seen at later reload samples, including the retry fired for the
separate three-second loosening horizon (#434). While a loosening is pending, the manager
retains its current and accepted snapshots, leaves `LastError` unchanged, and publishes
no reload result; daemon behavior and status therefore continue to reflect the active
last-good config. A retry makes a deliberate blank, deletion, or trailing-line deletion
take effect after the horizon even if no further filesystem event arrives. If a retry
observes a different loosening snapshot, it starts a fresh horizon and arms another retry
without relying on an fsnotify event. Transitions to `false`, non-`enabled` changes that
retain `enabled = false`, and explicit `enabled = true` keep the normal settle latency.
Reload attempts are serialized so concurrent fsnotify events cannot commit stale
candidates around the guard.

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

A candidate that becomes provisional-stable partway through the normal budget also
cannot reach acceptance in that call: reload is a no-op, while standalone loads retry
with the new content as their first snapshot. With no accepted baseline, a stable
non-empty partial cannot be recognized as a strict prefix; it receives the normal
two-read settle check.

`ResolvedTool.DirectoryAllowed` applies the effective directory privacy policy but does
not format paths for display. Display reduction belongs to the presence mapping boundary,
so config does not expose a second directory formatter that could diverge from it.

Discord-facing config is rejected during load when it cannot produce a valid activity.
Tool and custom-tool buttons allow at most two entries; labels are non-empty and at most
32 characters, and URLs are absolute HTTP(S) URLs. Details/fallback text and custom-tool
identity, display, and resolved image fields are bounded before registry construction;
custom-tool display names must contain 2–128 characters.
The feedback target is likewise bounded and restricted to an absolute HTTP(S) URL.
Config reads are capped at 1 MiB before TOML decoding.

Most watch tests write config changes atomically so they exercise malformed content
rather than a truncation window. Dedicated regression tests use deliberately divergent,
chunked, shrinking, unlink/recreate, and rename/append writers. The destructive-load
regression sweeps truncate stalls of 50ms, 250ms, 400ms, and 1s across both sides of the
normal settle budget, then asserts the saved TOML still contains `enabled = false` and
the user's `pin`. A fixed-seed randomized writer-schedule property test asserts that a
save whose final content sets `enabled = false` never exposes `enabled = true` before
completion. Schedules stalled beyond the loosening horizon are intentionally excluded
as the documented residual. A separate 2ms continuously rewriting fixture asserts
read-only and destructive standalone loads return under an explicit test timeout, with
the destructive path producing `ErrConfigBeingWritten`; command-level coverage asserts
setup and settings leave the file byte-identical when they receive that error.

`InitFile` uses `Lstat` and refuses symlinks and every other non-regular destination even
with `force`. It writes a temporary file in the destination directory and atomically
renames it. New files are created `0600`; forced replacement of an existing regular file
preserves that file's permission bits. Migration copies also preserve the source mode.
Without `force`, an existing regular file is not replaced.

**Depends on / used by:** Uses BurntSushi TOML and the standard library. The CLI,
detector, presence mapping, TUI, registry construction, and update policy consume it.

**Open questions / TODO:** None currently.
