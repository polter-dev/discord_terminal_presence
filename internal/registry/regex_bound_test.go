package registry

import (
	"regexp"
	"testing"
)

// TestCompileUserRegexRejectsOverlongPattern pins the length-cap half of
// #577: a user-supplied match/exclude regex used to have no length bound,
// so a modest value could still expand to a large compiled program and make
// every process-scan match expensive (measured at #577: a 240-byte value
// against a 4000-character process identity cost about 44ms per match).
func TestCompileUserRegexRejectsOverlongPattern(t *testing.T) {
	tooLong := ""
	for i := 0; i < MaxCustomRegexLength+1; i++ {
		tooLong += "a"
	}
	if _, err := compileUserRegex("match.regex", tooLong); err == nil {
		t.Fatalf("compileUserRegex accepted a %d-rune pattern, want rejection at %d", len(tooLong), MaxCustomRegexLength)
	}

	atLimit := tooLong[:MaxCustomRegexLength]
	if _, err := compileUserRegex("match.regex", atLimit); err != nil {
		t.Fatalf("compileUserRegex rejected a pattern exactly at the %d-rune limit: %v", MaxCustomRegexLength, err)
	}
}

// TestValidateCustomToolRejectsOverlongRegex proves the length cap is wired
// into the config-load boundary, not just the internal helper.
func TestValidateCustomToolRejectsOverlongRegex(t *testing.T) {
	tooLong := ""
	for i := 0; i < MaxCustomRegexLength+1; i++ {
		tooLong += "a"
	}
	err := ValidateCustomTool(CustomTool{
		DisplayName: "Custom",
		Match:       CustomMatch{Regex: tooLong},
	})
	if err == nil {
		t.Fatal("ValidateCustomTool accepted an overlong match.regex, want rejection")
	}
}

// TestCompileUserRegexRejectsWrapperGroupClosingValue pins the secondary
// #577 note: user regexes are wrapped as "(?i:" + value + ")" before being
// compiled for matching, so a value that is not a valid regex on its own can
// still compile successfully by closing the wrapper group early. "a)|(.*"
// is exactly such a value: it fails to compile standalone (unexpected ")")
// but compiles fine once concatenated inside the wrapper.
func TestCompileUserRegexRejectsWrapperGroupClosingValue(t *testing.T) {
	const sneaky = "a)|(.*"

	// Confirm the premise: the raw value is invalid on its own, but the
	// wrapped form compiles fine, which is exactly what makes this worth
	// rejecting explicitly rather than trusting the wrapped compile alone.
	if _, err := regexp.Compile(sneaky); err == nil {
		t.Fatalf("premise check: %q compiled standalone, want a parse error", sneaky)
	}
	if _, err := regexp.Compile("(?i:" + sneaky + ")"); err != nil {
		t.Fatalf("premise check: wrapped %q failed to compile: %v", sneaky, err)
	}

	if _, err := compileUserRegex("match.regex", sneaky); err == nil {
		t.Fatalf("compileUserRegex accepted %q, which is only valid because it closes the wrapper group early", sneaky)
	}
}

// TestCompileUserRegexAcceptsOrdinaryPatterns proves the two new checks do
// not over-reject: normal match/exclude regexes well under the length cap
// and valid standalone must still compile.
func TestCompileUserRegexAcceptsOrdinaryPatterns(t *testing.T) {
	patterns := []string{
		"^my-tool(-cli)?$",
		`(?:^|/)node_modules/\.bin/my-tool$`,
		"vim|nvim",
		".*",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			if _, err := compileUserRegex("match.regex", p); err != nil {
				t.Fatalf("compileUserRegex(%q) = %v, want no error", p, err)
			}
		})
	}
}
