// Package terminaltext sanitizes externally derived text before terminal output.
package terminaltext

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize removes terminal escape sequences, control characters, and Unicode
// bidirectional formatting controls from value.
func Sanitize(value string) string {
	value = ansi.Strip(value)
	var cleaned strings.Builder
	cleaned.Grow(len(value))
	for _, r := range value {
		if r <= 0x1f || r == 0x7f || r >= 0x80 && r <= 0x9f ||
			r == 0x061c || r == 0x200e || r == 0x200f ||
			r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			continue
		}
		cleaned.WriteRune(r)
	}
	return cleaned.String()
}

// separatorSentinel is a stand-in control character used internally while
// collapsing and trimming line-break substitutions. It is never a valid
// separator source itself, so it cannot collide with the characters being
// normalized. If it somehow survives to the final Sanitize call, it is a
// C0 control character and gets stripped like any other.
const separatorSentinel = "\x1e"

// visibleSeparator is what every recognized line-break character collapses
// down to in SanitizeSingleLine output.
const visibleSeparator = " ; "

// lineBreakReplacer maps every character or sequence that a terminal, log
// pipeline, or renderer could treat as a line/record break to the sentinel.
// CRLF must precede CR and LF so the two-byte sequence collapses to a single
// separator instead of two. Covered: CRLF, CR, LF, vertical tab (0x0B), form
// feed (0x0C), NUL (0x00), NEL (U+0085), LINE SEPARATOR (U+2028), and
// PARAGRAPH SEPARATOR (U+2029). See docs/context/terminaltext.md for the
// authoritative coverage table.
var lineBreakReplacer = strings.NewReplacer(
	"\r\n", separatorSentinel,
	"\r", separatorSentinel,
	"\n", separatorSentinel,
	"\v", separatorSentinel,
	"\f", separatorSentinel,
	"\x00", separatorSentinel,
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
