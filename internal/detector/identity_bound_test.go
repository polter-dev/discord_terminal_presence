package detector

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBoundIdentityField_NoOpUnderLimit ensures short, realistic identity
// fields are never altered.
func TestBoundIdentityField_NoOpUnderLimit(t *testing.T) {
	short := "node"
	if got := boundIdentityField(short); got != short {
		t.Fatalf("boundIdentityField(%q) = %q, want unchanged", short, got)
	}
}

// TestBoundIdentityField_TruncatesAtByteLimit reproduces #565: an unbounded
// Cmdline built from a huge argv reaches the registry matcher at full size.
// Before the fix this string passes through processIdentity untouched; a
// multi-MiB Cmdline then costs on the order of 360ns/byte in the matcher,
// pushing a single scan well past the default 3s interval.
func TestBoundIdentityField_TruncatesAtByteLimit(t *testing.T) {
	huge := strings.Repeat("a", 2*1024*1024) // 2 MiB, as in the reported repro
	got := boundIdentityField(huge)
	if len(got) > maxIdentityFieldBytes {
		t.Fatalf("boundIdentityField returned %d bytes, want <= %d", len(got), maxIdentityFieldBytes)
	}
	if len(got) == 0 {
		t.Fatalf("boundIdentityField returned empty string for non-empty input")
	}
}

// TestBoundIdentityField_DoesNotSplitUTF8Rune constructs an input where a
// multi-byte rune straddles the exact byte boundary the bound would cut at,
// and asserts the result is still valid UTF-8 (no split rune emitted).
func TestBoundIdentityField_DoesNotSplitUTF8Rune(t *testing.T) {
	// Fill up to one byte short of the limit with ASCII, then place a 3-byte
	// rune (e.g. "☃" SNOWMAN) straddling the cut point.
	prefix := strings.Repeat("a", maxIdentityFieldBytes-1)
	input := prefix + "☃" + strings.Repeat("b", 16)

	got := boundIdentityField(input)
	if len(got) > maxIdentityFieldBytes {
		t.Fatalf("boundIdentityField returned %d bytes, want <= %d", len(got), maxIdentityFieldBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("boundIdentityField split a UTF-8 rune: %q", got)
	}
}
