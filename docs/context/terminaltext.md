# terminaltext (package `internal/terminaltext`)

**Purpose:** Sanitizes externally derived strings before they cross a terminal or log
rendering boundary.

**Public surface:** `Sanitize` removes ANSI/OSC escapes, C0/C1 controls, and Unicode
bidirectional formatting controls while preserving ordinary Unicode text.

**Key files:** `internal/terminaltext/sanitize.go` contains the shared sanitizer.

**Invariants / gotchas:** CLI status output, verbose logging, and TUI rendering must pass
externally derived text through this package. Sanitization is a rendering-boundary
defense and does not replace validation or directory privacy resolution.

**Depends on / used by:** Depends on Charm's ANSI parser; used by `cmd/termp` and
`internal/tui`.

**Open questions / TODO:** None currently.
