package terminaltext

import (
	"strings"
	"testing"
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

func TestSanitizeSingleLine(t *testing.T) {
	nel := ""
	ls := " "
	ps := " "
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
