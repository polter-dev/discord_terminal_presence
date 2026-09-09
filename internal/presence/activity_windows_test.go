//go:build windows

package presence

import "testing"

// TestDirectoryDisplayHomeRedactionWindows exercises DirectoryDisplay's home
// redaction with real Windows-style backslash paths. filepath is POSIX-only
// when tests run on macOS/Linux, so a literal "C:\Users\alice" path compared
// on darwin would never take the backslash-aware code paths; this file is
// built only under GOOS=windows so filepath.Clean/Base/Dir/Separator behave
// as they would on real Windows hardware. Compiled and vetted only via
// GOOS=windows cross-compilation in this harness — not executed on real
// Windows hardware.
func TestDirectoryDisplayHomeRedactionWindows(t *testing.T) {
	tests := []struct {
		name         string
		cwd          string
		home         string
		basenameOnly bool
		want         string
	}{
		{name: "home exactly matches, basename-only mode", cwd: `C:\Users\alice`, home: `C:\Users\alice`, basenameOnly: true, want: "~"},
		{name: "home exactly matches, two-segment mode", cwd: `C:\Users\alice`, home: `C:\Users\alice`, basenameOnly: false, want: "~"},
		{name: "home has a trailing separator", cwd: `C:\Users\alice`, home: `C:\Users\alice\`, basenameOnly: true, want: "~"},
		{name: "parent equals home renders tilde-prefixed name", cwd: `C:\Users\alice\myproject`, home: `C:\Users\alice`, basenameOnly: false, want: "~/myproject"},
		{name: "grandchild of home is unaffected in two-segment mode", cwd: `C:\Users\alice\a\b`, home: `C:\Users\alice`, basenameOnly: false, want: "a/b"},
		{name: "sibling sharing a name prefix is not treated as home", cwd: `C:\Users\alice2`, home: `C:\Users\alice`, basenameOnly: true, want: "alice2"},
		{name: "a case-different path is not treated as home", cwd: `C:\Users\Alice`, home: `C:\Users\alice`, basenameOnly: true, want: "Alice"},
		{name: "empty home falls back to prior basename-only behavior", cwd: `C:\Users\alice`, home: "", basenameOnly: true, want: "alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DirectoryDisplay(tt.cwd, tt.home, tt.basenameOnly)
			if got != tt.want {
				t.Fatalf("DirectoryDisplay(%q, %q, %v) = %q, want %q", tt.cwd, tt.home, tt.basenameOnly, got, tt.want)
			}
		})
	}
}
