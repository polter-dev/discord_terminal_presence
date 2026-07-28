// Package terminaltext sanitizes externally derived text before terminal output.
package terminaltext

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// IsControlOrBidi reports whether r is a C0/C1 control character, DEL, or a
// Unicode bidirectional formatting control. This is the exact rune class
// Sanitize strips; it is exported so other packages (e.g. registry, which
// rejects these at config load rather than silently stripping them) can
// classify a rune the same way without duplicating or drifting from this
// definition.
func IsControlOrBidi(r rune) bool {
	return r <= 0x1f || r == 0x7f || r >= 0x80 && r <= 0x9f ||
		r == 0x061c || r == 0x200e || r == 0x200f ||
		r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069
}

// Sanitize removes terminal escape sequences, control characters, and Unicode
// bidirectional formatting controls from value.
func Sanitize(value string) string {
	value = ansi.Strip(value)
	var cleaned strings.Builder
	cleaned.Grow(len(value))
	for _, r := range value {
		if IsControlOrBidi(r) {
			continue
		}
		cleaned.WriteRune(r)
	}
	return cleaned.String()
}

// separatorSentinel is a stand-in control character (RS, 0x1E) used
// internally while collapsing and trimming line-break substitutions. RS is
// itself one of the characters lineBreakReplacer folds (deliberately: it is
// the record-separator control code, and gluing the tokens on either side of
// it back together is the exact `.debsudo`-style bug class this package
// exists to prevent), so mapping it to itself is intentional, not an
// accident of the sentinel mechanism. If it somehow survives to the final
// Sanitize call, it is a C0 control character and gets stripped like any
// other.
const separatorSentinel = "\x1e"

// visibleSeparator is what every recognized line-break character collapses
// down to in SanitizeSingleLine output.
const visibleSeparator = " ; "

// lineBreakReplacer maps every character or sequence that a terminal, log
// pipeline, or renderer could treat as a line/record break to the sentinel.
// CRLF must precede CR and LF so the two-byte sequence collapses to a single
// separator instead of two. Covered: CRLF, CR, LF, vertical tab (0x0B), form
// feed (0x0C), NUL (0x00), RS (0x1E, the sentinel itself, mapped to itself
// so the fold is explicit rather than an accident of sentinelRun also
// matching raw RS), NEL (U+0085), LINE SEPARATOR (U+2028), and PARAGRAPH
// SEPARATOR (U+2029). US (0x1F), RS's sibling separator code, is deliberately
// NOT folded here; it is simply stripped by Sanitize like any other control
// character. See docs/context/terminaltext.md for the authoritative coverage
// table.
var lineBreakReplacer = strings.NewReplacer(
	"\r\n", separatorSentinel,
	"\r", separatorSentinel,
	"\n", separatorSentinel,
	"\v", separatorSentinel,
	"\f", separatorSentinel,
	"\x00", separatorSentinel,
	"\x1e", separatorSentinel,
	"\u0085", separatorSentinel,
	"\u2028", separatorSentinel,
	"\u2029", separatorSentinel,
)

// sentinelRun matches any maximal stretch of spaces/tabs and sentinels that
// contains at least one sentinel, so adjacent line breaks (e.g. a blank
// line) and any padding around them collapse to one separator instead of
// leaving doubled or dangling punctuation.
var sentinelRun = regexp.MustCompile(`[ \t` + separatorSentinel + `]*` + separatorSentinel + `[ \t` + separatorSentinel + `]*`)

// SanitizeSingleLine sanitizes value while replacing line breaks with a
// visible separator so adjacent lines cannot be joined into a different
// token. Runs of separators (including ones produced by blank lines) collapse
// to a single separator, and leading/trailing separators are trimmed so
// substitution never leaves dangling punctuation at the edges. Sanitize runs
// last, so substitution can only ever break an escape sequence apart, never
// assemble one.
func SanitizeSingleLine(value string) string {
	value = lineBreakReplacer.Replace(value)
	value = sentinelRun.ReplaceAllString(value, separatorSentinel)
	value = strings.Trim(value, separatorSentinel)
	value = strings.ReplaceAll(value, separatorSentinel, visibleSeparator)
	return Sanitize(value)
}
