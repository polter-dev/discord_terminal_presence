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

`InitFile` uses `Lstat` and refuses symlinks and every other non-regular destination even
with `force`. It writes a temporary file in the destination directory and atomically
renames it. New files are created `0600`; forced replacement of an existing regular file
preserves that file's permission bits. Migration copies also preserve the source mode.
Without `force`, an existing regular file is not replaced.

**Depends on / used by:** Uses BurntSushi TOML and the standard library. The CLI,
detector, presence mapping, TUI, registry construction, and update policy consume it.

**Open questions / TODO:** None currently.
