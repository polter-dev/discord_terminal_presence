// Package terminaltext sanitizes externally derived text before terminal output.
package terminaltext

import (
	"regexp"
	"strings"
	"unicode/utf8"

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
// bidirectional formatting controls from value. Complete escape sequences are
// removed conservatively. If a sequence reaches end-of-input unterminated,
// only its introducer is removed; its payload is sanitized as ordinary text.
func Sanitize(value string) string {
	value = stripANSIWithBoundedSequences(value)
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

// stripANSIWithBoundedSequences preserves ansi.Strip's conservative behavior
// for every sequence the decoder reports as complete. When decoding reaches
// end-of-input in a non-normal state, the sequence is incomplete. Removing its
// generic introducer and decoding the remainder from NormalState makes the
// payload ordinary input instead of allowing the unfinished sequence to
// consume it.
func stripANSIWithBoundedSequences(value string) string {
	var bounded strings.Builder
	bounded.Grow(len(value))

	for len(value) > 0 {
		sequence, n, state := decodeTerminalSequence(value)
		if n == 0 {
			// DecodeSequence can make no progress on an invalid UTF-8 byte.
			// Retain it here so ansi.Strip preserves its existing handling.
			bounded.WriteByte(value[0])
			value = value[1:]
			continue
		}

		if state != ansi.NormalState {
			introducerLen := 1
			if sequence[0] == ansi.ESC && len(sequence) > 1 {
				introducerLen = 2
			}
			value = sequence[introducerLen:]
			continue
		}

		bounded.WriteString(sequence)
		value = value[n:]
	}

	return ansi.Strip(bounded.String())
}

// decodeTerminalSequence delegates sequence recognition to ansi.DecodeSequence.
// The decoder operates byte-by-byte in string payloads, so an 0x9c UTF-8
// continuation byte can look like an 8-bit ST. Continue the same decode when
// that byte belongs to a multi-byte rune; Charm documents that callers must
// validate returned string-sequence terminators.
func decodeTerminalSequence(value string) (sequence string, n int, state byte) {
	sequence, _, n, state = ansi.DecodeSequence(value, ansi.NormalState, nil)
	isControlSequence := n > 0 &&
		(sequence[0] == ansi.ESC || sequence[0] >= 0x80 && sequence[0] <= 0x9f)
	for isControlSequence && state == ansi.NormalState && n > 0 && sequence[n-1] == ansi.ST {
		if !endsWithUTF8Continuation(sequence) {
			break
		}

		remainder, _, consumed, nextState := ansi.DecodeSequence(value[n:], ansi.StringState, nil)
		sequence += remainder
		n += consumed
		state = nextState
		if consumed == 0 {
			break
		}
	}
	return sequence, n, state
}

func endsWithUTF8Continuation(value string) bool {
	_, size := utf8.DecodeLastRuneInString(value)
	if size > 1 {
		return true
	}

	start := len(value) - 1
	for start > 0 && !utf8.RuneStart(value[start]) {
		start--
	}
	return start < len(value)-1 && !utf8.FullRuneInString(value[start:])
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
