//go:build linux

package presence

import "testing"

// TestDirectoryDisplayHomeRedactionLinux pins the exact-match half of the home
// redaction: Linux filesystems are case-sensitive by default, so /home/Alice
// and /home/alice are genuinely different directories and the first must NOT
// render as "~" for a home of the second; doing so would present another
// user's directory as this user's home. Note the value it does render is that
// other directory's own basename, not this user's account name, so this is not
// the #620 leak. Compiled and vetted via GOOS=linux cross-compilation in the
// macOS harness; executed for real only by the ubuntu CI job. The folding
// counterparts are activity_darwin_test.go and activity_windows_test.go.
func TestDirectoryDisplayHomeRedactionLinux(t *testing.T) {
	tests := []struct {
		name         string
		cwd          string
		home         string
		basenameOnly bool
		want         string
	}{
		{name: "a case-different path is a different directory", cwd: "/home/Alice", home: "/home/alice", basenameOnly: true, want: "Alice"},
		{name: "a case-different path is a different directory, two-segment mode", cwd: "/home/Alice", home: "/home/alice", basenameOnly: false, want: "home/Alice"},
		{name: "a case-different parent is not home", cwd: "/home/Alice/myproject", home: "/home/alice", basenameOnly: false, want: "Alice/myproject"},
		{name: "an exact match is still home", cwd: "/home/alice", home: "/home/alice", basenameOnly: true, want: "~"},
		{name: "an exact parent match still renders tilde-prefixed name", cwd: "/home/alice/myproject", home: "/home/alice", basenameOnly: false, want: "~/myproject"},
		{name: "a sibling sharing a name prefix is not treated as home", cwd: "/home/alice2", home: "/home/alice", basenameOnly: true, want: "alice2"},
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
