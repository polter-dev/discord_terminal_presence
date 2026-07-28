# terminaltext (package `internal/terminaltext`)

**Purpose:** Sanitizes externally derived strings before they cross a terminal or log
rendering boundary.

**Public surface:** `Sanitize` is a conservative terminal-safety filter: it removes
ANSI/OSC escapes, C0/C1 controls, and Unicode bidirectional formatting controls while
preserving ordinary Unicode text. It may remove bytes adjacent to an escape introducer
when Charm recognizes them as part of a complete sequence; for example, `ESC` plus a
letter can be removed together. Its collateral damage is bounded, however: string
sequences (OSC, DCS, APC, PM, and SOS, including their 8-bit C1 forms) are removed whole
only when terminated by BEL or ST (`ESC \` or the 8-bit ST byte). If ESC is not followed
by `\`, or CAN, SUB, or end-of-input is reached first, the sequence is aborted.
`Sanitize` then removes only its introducer and passes the payload through the same
ordinary escape/control/bidi filtering instead of consuming the rest of the value.
`SanitizeSingleLine` additionally folds every recognized line/record-break character into
a single visible ` ; ` separator, collapsing runs and trimming leading/trailing
separators, before sanitizing. `IsControlOrBidi(r rune) bool` exports the exact per-rune
predicate `Sanitize` strips by, so a caller that needs to *reject* rather than silently
strip (e.g. `internal/registry` at config load) classifies a rune identically instead of
maintaining a second copy of the rule that can drift from this one.

**Separator coverage (exact):** `SanitizeSingleLine` normalizes CRLF, CR (`\r`), LF
(`\n`), vertical tab (`\v`, 0x0B), form feed (`\f`, 0x0C), NUL (0x00), RS (0x1E, RECORD
SEPARATOR — this is also the internal sentinel byte; it is folded deliberately, since
gluing the tokens on either side of it back together is the exact `.debsudo`-style bug
class this package exists to prevent), NEL (U+0085), LINE SEPARATOR (U+2028), and
PARAGRAPH SEPARATOR (U+2029) — every character a mainstream terminal or log pipeline
could treat as ending a line/record. CRLF is matched before the lone CR/LF entries so it
collapses to one separator, not two. US (0x1F, UNIT SEPARATOR, RS's sibling control code)
is deliberately **not** folded — it is simply stripped by the trailing `Sanitize` call
like any other C0/C1 control character, so `"a\x1fb"` becomes `"ab"` while `"a\x1eb"`
becomes `"a ; b"`. Other than that one documented asymmetry, anything not in the folded
list either isn't a line-break candidate or is already removed by `Sanitize`. Consecutive
separators (e.g. from a blank line) collapse to one, and leading/trailing separators are
trimmed, so substitution never leaves dangling or doubled punctuation at the edges.
Tests spell these separator controls with Unicode escapes so static analysis can inspect
the source without encountering literal control characters.

**Key files:** `internal/terminaltext/sanitize.go` contains the shared sanitizer.

**Invariants / gotchas:** CLI status output, verbose logging, daemon and interactive-watch
config warnings, and TUI rendering must pass externally derived text through this package.
Sanitization is a rendering-boundary defense and does not replace validation or directory
privacy resolution. Single-line status fields, log records, and the watch TUI's warning
banner use `SanitizeSingleLine`; multi-step values remain readable without joining tokens,
changing label-column alignment, or allowing a line break to inject another log record.
`Sanitize` itself continues to strip all control characters, including newlines. Complete,
properly terminated string sequences are removed whole. An aborted string sequence can
never swallow the remainder of the string: ESC (unless it begins ST), CAN, SUB, and
end-of-input abort it, its explicit 7-bit or 8-bit introducer is discarded, and its
payload is decoded again as ordinary input. Non-string terminal sequences continue to be
recognized by Charm's decoder; an incomplete non-string sequence at end-of-input receives
the same bounded treatment. `SanitizeSingleLine` always calls `Sanitize` last —
substitution can only ever break an escape sequence apart, never assemble or preserve one.
Consequently, sanitization is no longer monotonically shortening: for the same input,
`Sanitize` can return materially more text than before because an aborted sequence's
payload is now preserved (for example, `"proj\x1b]"` followed by `"a"` and 200 combining
acute accents grows from 4 runes to 205); this is not a new character exposure—every such
character is equally reachable in a name without an escape prefix—but callers that reason
about output length, particularly `internal/presence`, which bounds text before sanitizing,
must account for it.
(An escape sequence, or an OSC title, that already spanned what were two lines before
substitution is still stripped as a unit when properly terminated, exactly as a same-line
escape or OSC title is.)

**Depends on / used by:** Depends on Charm's ANSI parser; used by `cmd/termp` and
`internal/tui` at terminal/log rendering boundaries, `internal/presence` (`client.go`'s
`sanitizeActivity`) to sanitize every Discord-facing activity field before it is
validated or leaves the process, and `internal/registry` (`ValidateCustomTool`,
`ValidateButtons`, via `IsControlOrBidi`) to reject control characters in `display_name`,
`image_key`, and button labels at config load (#419).

**Open questions / TODO:** None currently.
