//go:build darwin

package presence

import "testing"

// TestDirectoryDisplayHomeRedactionDarwin pins the macOS-specific half of the
// home redaction: the default APFS and HFS+ volumes are case-insensitive, so a
// cwd whose casing differs from os.UserHomeDir() is still the home directory
// and must render as "~" rather than falling through to filepath.Base($HOME),
// the account name (#620). Folding must never widen into a prefix match: a
// sibling such as /Users/ALICE2 stays a distinct directory. The
// platform-independent rows live in TestDirectoryDisplayHomeRedaction; the
// exact-match Linux counterpart is activity_linux_test.go.
func TestDirectoryDisplayHomeRedactionDarwin(t *testing.T) {
	tests := []struct {
		name         string
		cwd          string
		home         string
		basenameOnly bool
		want         string
	}{
		{name: "a case-different path is home", cwd: "/Users/Alice", home: "/Users/alice", basenameOnly: true, want: "~"},
		{name: "a case-different path is home, two-segment mode", cwd: "/Users/Alice", home: "/Users/alice", basenameOnly: false, want: "~"},
		{name: "an all-caps path is home", cwd: "/USERS/ALICE", home: "/Users/alice", basenameOnly: true, want: "~"},
		{name: "a case-different home with trailing slash is home", cwd: "/Users/Alice", home: "/Users/alice/", basenameOnly: true, want: "~"},
		{name: "a case-different parent equal to home renders tilde-prefixed name", cwd: "/Users/ALICE/myproject", home: "/Users/alice", basenameOnly: false, want: "~/myproject"},
		{name: "a case-different grandchild of home is unaffected", cwd: "/Users/ALICE/a/b", home: "/Users/alice", basenameOnly: false, want: "a/b"},
		{name: "a case-different sibling sharing a name prefix is not treated as home", cwd: "/Users/ALICE2", home: "/Users/alice", basenameOnly: true, want: "ALICE2"},
		{name: "a case-different sibling, two-segment parent", cwd: "/Users/ALICE2/project", home: "/Users/alice", basenameOnly: false, want: "ALICE2/project"},
		{name: "a same-cased sibling sharing a name prefix is not treated as home", cwd: "/Users/alice2", home: "/Users/alice", basenameOnly: true, want: "alice2"},
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
