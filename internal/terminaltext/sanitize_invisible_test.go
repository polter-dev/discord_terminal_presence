package terminaltext

import "testing"

// TestIsControlOrBidiRejectsInvisibleFormatting pins the #571 fix: a set of
// codepoints that render as nothing (or as an innocuous-looking separator)
// while still being present in the string. Before #571 these all passed
// IsControlOrBidi (and therefore Sanitize, and registry's firstDisallowedRune,
// which is built on the same predicate) unrejected.
//
// Runes are spelled with \u/\U escapes, not literal characters, so static
// analysis and code review can inspect the source without encountering
// invisible bytes, matching this package's existing convention for control
// characters.
func TestIsControlOrBidiRejectsInvisibleFormatting(t *testing.T) {
	tests := []struct {
		name string
		r    rune
	}{
		{"zero width space", '\u200b'},
		{"zero width non-joiner", '\u200c'},
		{"zero width joiner", '\u200d'},
		{"word joiner", '\u2060'},
		{"invisible times", '\u2062'},
		{"invisible separator", '\u2063'},
		{"invisible plus", '\u2064'},
		{"byte order mark / zwnbsp", '\ufeff'},
		{"soft hyphen", '\u00ad'},
		{"mongolian vowel separator", '\u180e'},
		{"combining grapheme joiner", '\u034f'},
		{"hangul choseong filler", '\u115f'},
		{"hangul filler", '\u3164'},
		{"interlinear annotation anchor", '\ufff9'},
		{"interlinear annotation separator", '\ufffa'},
		{"interlinear annotation terminator", '\ufffb'},
		{"line separator", '\u2028'},
		{"paragraph separator", '\u2029'},
		{"tags block start", '\U000e0001'},
		{"tags block ascii-range tag", '\U000e0041'},
		{"tags block end", '\U000e007f'},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !IsControlOrBidi(tt.r) {
				t.Fatalf("IsControlOrBidi(%U) = false, want true", tt.r)
			}
		})
	}
}

// TestSanitizeStripsInvisibleFormatting proves the class is actually removed
// by Sanitize, not merely classified by the predicate, and that it does not
// disturb the surrounding ordinary text.
func TestSanitizeStripsInvisibleFormatting(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "zero width space between words", input: "safe\u200bhidden", want: "safehidden"},
		{name: "zero width joiner splices adjacent text", input: "a\u200db", want: "ab"},
		{name: "byte order mark prefix", input: "\ufeffvalue", want: "value"},
		{name: "soft hyphen mid word", input: "soft\u00adhyphen", want: "softhyphen"},
		{name: "line separator", input: "line1\u2028line2", want: "line1line2"},
		{name: "paragraph separator", input: "para1\u2029para2", want: "para1para2"},
		{
			// The Unicode Tags block (U+E0000-U+E007F) is a known
			// text-smuggling channel: an entire ASCII path segment can be
			// encoded in tag codepoints that render as nothing, hiding a
			// path-traversal payload inside what looks like a plain slug.
			name:  "tags block hides a path segment",
			input: "git\U000e0041\U000e0042\U000e007f.png",
			want:  "git.png",
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

// TestIsControlOrBidiAcceptsLegitimateMultibyteText proves the invisible-
// formatting class added for #571 does not over-reject: ordinary combining
// marks, astral-plane emoji (single codepoint, no ZWJ splice), and normal
// multibyte script text must all still pass through untouched.
func TestIsControlOrBidiAcceptsLegitimateMultibyteText(t *testing.T) {
	// A representative sample, not exhaustive: combining diacritics,
	// non-Latin scripts, and standalone (non-ZWJ-joined) astral emoji.
	legit := "caf\u00e9 Z\u00fcrich na\u00efve \u65e5\u672c\u8a9e \ud55c\uae00 \u05e2\u05d1\u05e8\u05d9\u05ea " +
		"\u0627\u0644\u0639\u0631\u0628\u064a\u0629 \u03a9 \u041a\u0438\u0440\u0438\u043b\u043b\u0438\u0446\u0430 " +
		"\u0e44\u0e17\u0e22 \u0ba4\u0bae\u0bbf\u0bb4\u0bcd \U0001f680\U0001f389\U0001f44d"
	for _, r := range legit {
		if IsControlOrBidi(r) {
			t.Fatalf("IsControlOrBidi(%U) = true, want false for legitimate rune in %q", r, legit)
		}
	}
	if got := Sanitize(legit); got != legit {
		t.Fatalf("Sanitize(%q) = %q, want unchanged", legit, got)
	}
}
