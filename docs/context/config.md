# config (package `internal/config`)

**Purpose:** Defines termp's user configuration, defaults, validation, path migration,
serialization, and hot-reload manager.

**Public surface:** `Config` and its nested UI/display/privacy/CTA/tool types are the
runtime schema. `Default`, `DefaultPath`, `Load`, `LoadPath`, and `Save` resolve and
persist it. `AnnotatedSample` and `InitFile(path, force)` support `termp config init`.
`Manager` watches a path and publishes validated changes.

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

`Manager.Reload` defends against non-atomic (truncate-then-write) saves, which is what a
transient empty or partial file — often still syntactically valid TOML on its own — comes
from: before accepting a read as a reload result, it waits (via `settledConfigSnapshot`)
for two consecutive reads of the file to agree, reading every ~15ms, up to 20 attempts
(~300ms budget). This fixes #410: without it, an ordinary non-atomic editor save produced
a momentary 0-byte file that fsnotify observed and the manager accepted as last-good,
silently discarding the user's real settings (`enabled = false` included) in favor of
defaults. A file that never stops changing within the budget leaves last-good and
`LastError` untouched for that reload attempt (relying on the write's completion to fire
another fsnotify event); a deliberately blanked config that stays blank still settles and
loads defaults, so that legitimate reset path is unaffected. This is a mitigation, not a
guarantee: a writer that pauses longer than the poll interval mid-save can still settle
on a partial state — either a pause right after truncate (observed flipping
`enabled = false` back to the default `true`) or a pause mid-content that leaves a stable
but incomplete prefix (observed settling on one written field while a later field in the
same save was still pending). In both cases the write's later completion fires a further
fsnotify event that corrects last-good, but persistent loss remains theoretically
reachable on Linux if that final content happens to be invalid TOML. Tracked as a
follow-up to #410.

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
rather than a truncation window; a dedicated set of tests (see `nonAtomicWriter` in
`config_test.go`) writes non-atomically on purpose to cover the settle behavior above.

`InitFile` uses `Lstat` and refuses symlinks and every other non-regular destination even
with `force`. It writes a temporary file in the destination directory and atomically
renames it. New files are created `0600`; forced replacement of an existing regular file
preserves that file's permission bits. Migration copies also preserve the source mode.
Without `force`, an existing regular file is not replaced.

**Depends on / used by:** Uses BurntSushi TOML and the standard library. The CLI,
detector, presence mapping, TUI, registry construction, and update policy consume it.

**Open questions / TODO:** None currently.
