# config (package `internal/config`)

**Purpose:** Loads the XDG-aware TOML config, applies privacy-first defaults, validates
user custom tools, including slug-based logo config, resolves per-tool display/privacy
settings, owns headliner/collection knobs, and hot-reloads the file while keeping the
last-good config.

**Public surface:** `Default`, `DefaultPath`, `Load`, `LoadPath`, and `Save` provide config
loading and write-back. Top-level config includes `Pin`, `HeadlinerIdleTimeout`,
`ActivitySwitching`, `IdleClearTimeout`, `DetailsFormat`, and the soft-prototype `CTA`
presence button config; `Display.Collection` controls the `also:` state line.
`ScanIntervalDuration`, `IdleClearTimeoutDuration`, and `HeadlinerIdleTimeoutDuration`
parse duration strings with safe defaults. `Config.Resolve` returns effective
`ResolvedTool` settings for a registry tool. `ResolvedTool.DirectoryAllowed` and
`DisplayDirectory` apply directory privacy rules. `FeedbackURL` is the config-driven URL
opened by the settings TUI feedback action. `Manager` exposes `Current`,
`LastError`, `Reload`, `Watch`, and `Changes` for concurrency-safe hot reload.

**Key files:** `internal/config/config.go` contains schema structs, TOML decode/encode,
validation, resolution, and privacy helpers. `internal/config/manager.go` owns the
RWMutex-protected last-good config and fsnotify watcher. `internal/config/config_test.go`
covers defaults, valid load, save/load round-trip, resolution order, privacy, validation,
and malformed reload behavior.

**Invariants / gotchas:** Missing config file is not an error. `Save` creates the parent
directory and uses temp-file-plus-rename atomic write; comments are not preserved. Directory display defaults
off and basename-only defaults on. Collection display defaults on but loses the single
Discord `state` line whenever an allowed directory is displayed. `Pin` is a tool ID string
and defaults empty. `HeadlinerIdleTimeout` defaults to `"60s"`, and
`ActivitySwitching` defaults true. `IdleClearTimeout` defaults to `"0"` so AFK clear is
disabled unless explicitly enabled, and `DetailsFormat` defaults to `"Using {tool}"`.
Per-tool bool fields are pointers so unset falls through to global defaults. Per-tool
`buttons` is a button-list override for registry defaults; global `[display].buttons`
remains the show/hide toggle. Custom tools require `image_url`, `image_key`, or
`icon_slug`; TOML `icon_slug`/`icon_source` are passed through to the registry resolver so
slug-only custom tools can use automatic CDN logos. `[cta]` defaults on for issue #10
prototype visibility; its "What is this?" button defaults to the live termp landing page
at `https://termp.polter.sh/`.
Top-level `feedback_url` defaults to the issue #16 placeholder/dead-link URL
`https://termp.example/feedback` so it can be swapped without rebuilding. Unknown TOML
keys produce warnings, not load failure. Malformed TOML or invalid `custom_tools` is
surfaced as an error while `Manager` keeps its previous last-good config.

**Depends on / used by:** Imports `internal/registry` only for `CustomTool`, `CustomMatch`, and `Button` shapes. Intended consumers are `cmd/termpresence` now and `internal/presence` after M2 integration.

**Open questions / TODO:** CLI currently reapplies display changes on config reload, but
changes to detector-affecting settings such as custom tools, pin, scan interval, and
activity switching still require daemon restart.
