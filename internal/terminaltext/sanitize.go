// Package terminaltext sanitizes externally derived text before terminal output.
package terminaltext

import (
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
