package terminaltext

import "testing"

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
	input := "first\x1b[31m\r\nsecond\nthird\rfourth"
	want := "first ; second ; third ; fourth"
	if got := SanitizeSingleLine(input); got != want {
		t.Fatalf("SanitizeSingleLine(%q) = %q, want %q", input, got, want)
	}
}
