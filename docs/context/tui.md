# tui (package `internal/tui`)

**Purpose:** Owns terminal UI models and shared renderers for setup, settings, watch, and
the Discord-card preview.

**Public surface:** `NewSettingsModel` creates the progressive settings editor.
`NewSetupModel` creates onboarding and `WithCompletion` adds its default-off completion
choice. `ConfirmDialog` provides reusable Yes/No confirmation. `RenderCard` is the pure
preview renderer. `NewWatchModel*`, `ActivityMsg`, and `ConnMsg` form the live-watch
boundary.

**Key files:** `internal/tui/settings.go` contains Miller-style columns, fuzzy pin search,
usage-ranked results, save, and feedback. `setup.go` applies config/autostart/completion.
`styles.go` owns the adaptive palette. `card.go` owns card rendering and uses the shared
`internal/terminaltext` sanitizer. `confirm.go` and `watch.go` contain their respective
models.

**Invariants / gotchas:** Settings and setup sanitize externally derived values at shared
rendering boundaries, not at individual call sites. Table cells inherit a Lip Gloss
`Transform` that strips unsafe terminal text; status, error, path, and setup-summary
strings use the shared render helper. ANSI escapes, C0/C1 controls, and bidirectional
controls from config, paths, metadata, or errors must not reach the terminal.

Setup persists config before reconciling autostart and rolls config back if autostart
fails. Completion installation runs afterward as an optional outcome. If only completion
installation fails, setup is still applied: config and autostart remain in effect, the
model adopts the persisted config, and the summary reports completion failure as partial
success.

The settings view clips the whole output to terminal width, drops leftmost columns when
necessary, never wraps/truncates mid-glyph, and keeps focused ancestry when it fits.
Section-label rows are not selectable. Pin search returns at most six results and ranks
exact, prefix, substring, subsequence, then bounded Levenshtein matches; no match renders
an explicit row. The feedback action revalidates its target as a bounded absolute HTTP(S)
URL immediately before calling the platform opener.

`RenderCard` does no I/O and never renders a raw asset URL. Watch stores already-resolved
activities, caps recent featured-tool changes at five, and renders a persistent sanitized
warning banner when its caller supplies one. Setup confirmation/navigation semantics and
Ctrl+C's immediate exit must remain stable.

**Depends on / used by:** Depends on Bubble Tea, Lip Gloss, `internal/config`,
`internal/registry`, and `internal/presence`; used by `cmd/termp`.

**Open questions / TODO:** Embed `RenderCard` in settings when preview work is scheduled.
