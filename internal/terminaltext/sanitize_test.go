package terminaltext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain text", input: "Neovim 日本語", want: "Neovim 日本語"},
		{name: "OSC title", input: "safe\x1b]0;title\x07 text", want: "safe text"},
		{name: "CSI color", input: "\x1b[31mred\x1b[0m", want: "red"},
		{name: "bare control", input: "bad\x03value", want: "badvalue"},
		{
			name:  "bidi formatting controls",
			input: "safe\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069text",
			want:  "safetext",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.input); got != tt.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeEscapeSequenceBounds(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// A conservative filter may remove a complete sequence or the
		// introducer of an incomplete one. It must not discard payload merely
		// because the sequence reached end-of-input without a terminator.
		{name: "plain text is unchanged", input: "alpha 日本語 omega", want: "alpha 日本語 omega"},
		{name: "lone ESC is removed", input: "a\x1b", want: "a"},
		{name: "complete ESC letter sequence is removed", input: "a\x1bZb", want: "ab"},
		{name: "screen ESC k remains conservatively bounded", input: "large\x1bkeyimg", want: "largeeyimg"},
		{name: "unterminated CSI loses its introducer but preserves parameters", input: "a\x1b[31", want: "a31"},
		{name: "unterminated OSC preserves payload", input: "a\x1b]title", want: "atitle"},
		{name: "BEL terminated OSC is removed whole", input: "a\x1b]0;title\x07b", want: "ab"},
		{name: "ST terminated OSC is removed whole", input: "a\x1b]0;title\x1b\\b", want: "ab"},
		{name: "unterminated DCS preserves payload", input: "a\x1bPqdata", want: "aqdata"},
		{name: "unterminated APC preserves payload", input: "a\x1b_payload", want: "apayload"},
		{name: "unterminated PM preserves payload", input: "a\x1b^payload", want: "apayload"},
		{name: "unterminated SOS preserves payload", input: "a\x1bXpayload", want: "apayload"},
		{name: "complete CSI is removed whole", input: "a\x1b[31mred", want: "ared"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.input); got != tt.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeUnterminatedSequenceDoesNotPreserveControls(t *testing.T) {
	var payload strings.Builder
	payload.WriteString("visible")
	for r := rune(0); r <= utf8.MaxRune; r++ {
		// These bytes alter the outer OSC's syntax, so they are not payload
		// in an OSC that remains unterminated through end-of-input.
		if r == '\x07' || r == '\x18' || r == '\x1a' || r == '\x1b' {
			continue
		}
		if IsControlOrBidi(r) {
			payload.WriteRune(r)
		}
	}

	got := Sanitize("\x1b]" + payload.String())
	if got != "visible" {
		t.Fatalf("Sanitize(unterminated OSC with every non-syntax control/bidi rune) = %q, want %q", got, "visible")
	}
	for _, r := range got {
		if IsControlOrBidi(r) {
			t.Fatalf("Sanitize output %q retained control/bidi rune %U", got, r)
		}
	}
}

func FuzzSanitizeUnterminatedSequencePreservesPayload(f *testing.F) {
	f.Add("ordinary payload")
	f.Add("controls\x00\x03\x1f\u0085remain filtered")
	f.Add("bidi\u061c\u202e\u2069remain filtered")
	f.Add("unicode 日本語 🦊")

	f.Fuzz(func(t *testing.T, payload string) {
		// Keep the generated OSC unterminated. Valid UTF-8 makes "character"
		// precise; ESC, BEL, CAN, and SUB are excluded because they can
		// terminate or cancel the outer OSC rather than belong to its payload.
		payload = strings.ToValidUTF8(payload, "\ufffd")
		payload = strings.NewReplacer("\x1b", "", "\x07", "", "\x18", "", "\x1a", "").Replace(payload)

		var want strings.Builder
		for _, r := range payload {
			if !IsControlOrBidi(r) {
				want.WriteRune(r)
			}
		}

		if got := Sanitize("\x1b]" + payload); got != want.String() {
			t.Fatalf("Sanitize(unterminated OSC + %q) = %q, want payload filtered to %q", payload, got, want.String())
		}
	})
}

func TestSanitizeSingleLine(t *testing.T) {
	nel := "\u0085"
	ls := "\u2028"
	ps := "\u2029"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "CRLF, LF, and CR all separate",
			input: "first\x1b[31m\r\nsecond\nthird\rfourth",
			want:  "first ; second ; third ; fourth",
		},
		{
			name:  "trailing newline leaves no dangling separator",
			input: "some error\n",
			want:  "some error",
		},
		{
			name:  "leading newline leaves no dangling separator",
			input: "\nsome error",
			want:  "some error",
		},
		{
			name:  "blank line does not double the separator",
			input: "Debian/Ubuntu: foo\n\nRPM-based Linux: bar",
			want:  "Debian/Ubuntu: foo ; RPM-based Linux: bar",
		},
		{
			name:  "n/a with trailing newline collapses to bare n/a",
			input: "n/a\n",
			want:  "n/a",
		},
		{
			name:  "vertical tab separates tokens instead of gluing them",
			input: "a\x0bb",
			want:  "a ; b",
		},
		{
			name:  "form feed separates tokens instead of gluing them",
			input: "a\x0cb",
			want:  "a ; b",
		},
		{
			name:  "NUL separates tokens instead of gluing them",
			input: "a\x00b",
			want:  "a ; b",
		},
		{
			name:  "NEL separates tokens instead of gluing them",
			input: "a" + nel + "b",
			want:  "a ; b",
		},
		{
			// RS (0x1E) is also the internal sentinel byte. It is folded
			// deliberately (not by accident of the sentinel mechanism): see
			// lineBreakReplacer and docs/context/terminaltext.md.
			name:  "RS (record separator) separates tokens instead of gluing them",
			input: "a\x1eb",
			want:  "a ; b",
		},
		{
			// US (0x1F), RS's sibling separator code, is deliberately NOT
			// folded into a visible separator -- it is just stripped like any
			// other control character by the trailing Sanitize call. This
			// asymmetry with RS is intentional and documented.
			name:  "US (unit separator) is stripped, not folded, unlike RS",
			input: "a\x1fb",
			want:  "ab",
		},
		{
			name:  "line separator U+2028 is folded into the separator",
			input: "a" + ls + "b",
			want:  "a ; b",
		},
		{
			name:  "paragraph separator U+2029 is folded into the separator",
			input: "a" + ps + "b",
			want:  "a ; b",
		},
		{
			// A lone ESC immediately followed by our " ; " separator forms a
			// valid (if obscure) ECMA-48 escape sequence (ESC, intermediate
			// SP, final ";"), which ansi.Strip removes as a unit inside the
			// final Sanitize call. Because Sanitize runs last, the leftover
			// "[31m" is just inert literal text with no ESC byte in front of
			// it -- it can never execute as a color code. This matches the
			// pre-existing behavior of the shipped (#387) implementation
			// against the same input; substitution only ever breaks escape
			// sequences apart, it never assembles or preserves one.
			name:  "escape sequence split across a separator never reassembles",
			input: "safe\x1b\n[31mtext",
			want:  "safe [31mtext",
		},
		{
			// An OSC sequence that spans what were two lines has its
			// payload -- including our substituted separator -- swallowed
			// and removed by ansi.Strip up to the BEL terminator, identical
			// to how a same-line OSC title is already fully dropped (see
			// TestSanitize's "OSC title" case). No escape byte survives.
			// This is also unchanged from the pre-existing (#387)
			// implementation for the same input.
			name:  "OSC sequence split across a separator never reassembles",
			input: "safe\x1b]0;\nhijack\x07text",
			want:  "safetext",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeSingleLine(tt.input)
			if got != tt.want {
				t.Fatalf("SanitizeSingleLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if strings.ContainsAny(got, "\r\n\v\f\x00") ||
				strings.Contains(got, nel) ||
				strings.Contains(got, ls) ||
				strings.Contains(got, ps) {
				t.Fatalf("SanitizeSingleLine(%q) = %q still contains a raw line-break rune", tt.input, got)
			}
			if strings.Contains(got, "\x1b") {
				t.Fatalf("SanitizeSingleLine(%q) = %q still contains an ESC byte", tt.input, got)
			}
		})
	}
}
