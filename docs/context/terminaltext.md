# terminaltext (package `internal/terminaltext`)

**Purpose:** Sanitizes externally derived strings before they cross a terminal or log
rendering boundary.

**Public surface:** `Sanitize` removes ANSI/OSC escapes, C0/C1 controls, and Unicode
bidirectional formatting controls while preserving ordinary Unicode text.
`SanitizeSingleLine` additionally folds every recognized line/record-break character into
a single visible ` ; ` separator, collapsing runs and trimming leading/trailing
separators, before sanitizing.

**Separator coverage (exact):** `SanitizeSingleLine` normalizes CRLF, CR (`\r`), LF
(`\n`), vertical tab (`\v`, 0x0B), form feed (`\f`, 0x0C), NUL (0x00), NEL (U+0085), LINE
SEPARATOR (U+2028), and PARAGRAPH SEPARATOR (U+2029) — every character a mainstream
terminal or log pipeline could treat as ending a line/record. CRLF is matched before the
lone CR/LF entries so it collapses to one separator, not two. No other character is
folded; anything not in this list either isn't a line-break candidate or is already
removed by the trailing `Sanitize` call (e.g. other C0/C1 controls). Consecutive
separators (e.g. from a blank line) collapse to one, and leading/trailing separators are
trimmed, so substitution never leaves dangling or doubled punctuation at the edges.

**Key files:** `internal/terminaltext/sanitize.go` contains the shared sanitizer.

**Invariants / gotchas:** CLI status output, verbose logging, daemon and interactive-watch
config warnings, and TUI rendering must pass externally derived text through this package.
Sanitization is a rendering-boundary defense and does not replace validation or directory
privacy resolution. Single-line status fields, log records, and the watch TUI's warning
banner use `SanitizeSingleLine`; multi-step values remain readable without joining tokens,
changing label-column alignment, or allowing a line break to inject another log record.
`Sanitize` itself continues to strip all control characters, including newlines, and
`SanitizeSingleLine` always calls it last — substitution can only ever break an escape
sequence apart, never assemble or preserve one. (An escape sequence, or an OSC title, that
already spanned what were two lines before substitution is still stripped as a unit by the
underlying ANSI parser, exactly as a same-line escape or OSC title already is; this is
unchanged pre-existing behavior, not something substitution introduces.)

**Depends on / used by:** Depends on Charm's ANSI parser; used by `cmd/termp` and
`internal/tui`.

**Open questions / TODO:** None currently.
