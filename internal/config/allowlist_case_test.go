package config

import (
	"os"
	"path/filepath"
	"testing"
)

// allowlistTool builds a resolved tool whose only privacy restriction is the
// given allowlist, so directoryAllowedOn's answer is entirely about path
// canonicalization.
func allowlistTool(entries ...string) ResolvedTool {
	return ResolvedTool{Enabled: true, ShowDirectory: true, DirectoryAllowlist: entries}
}

// TestDirectoryAllowlistCaseSensitivityPerPlatform covers #624: the directory
// allowlist compared paths case-sensitively everywhere except Windows, so on
// macOS's default (case-insensitive) APFS or HFS+ volume an entry written as
// "~/Projects" did not match a detected cwd reported as "/Users/alice/projects".
// That fails closed -- the directory is silently dropped from the published
// activity -- which is a usability defect, not a leak, but it is the same
// per-platform case-sensitivity assumption that caused #620.
//
// Linux and the BSDs must stay case-SENSITIVE: "/home/Alice" and "/home/alice"
// genuinely are different directories on ext4, XFS, btrfs, ZFS and UFS, so
// folding there would let one user's allowlist entry authorize another user's
// directory, which is the opposite (fail-open) defect.
//
// Every path here is under a nonexistent subdirectory of t.TempDir() so
// EvalSymlinks fails identically for all of them and the only variable under
// test is the case fold. Path arithmetic still uses the host's filepath, which
// is why the paths are host-shaped rather than Windows-shaped.
func TestDirectoryAllowlistCaseSensitivityPerPlatform(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-such-root")
	entry := filepath.Join(root, "Projects")

	tests := []struct {
		name string
		cwd  string
		// want, keyed by whether the platform folds case.
		wantFolding   bool
		wantNoFolding bool
	}{
		{
			name:          "exact spelling",
			cwd:           filepath.Join(root, "Projects", "termp"),
			wantFolding:   true,
			wantNoFolding: true,
		},
		{
			name:          "cwd differs from the entry only by case",
			cwd:           filepath.Join(root, "projects", "termp"),
			wantFolding:   true,
			wantNoFolding: false,
		},
		{
			name:          "entry itself, differently cased",
			cwd:           filepath.Join(root, "PROJECTS"),
			wantFolding:   true,
			wantNoFolding: false,
		},
		{
			name:          "genuinely different sibling directory",
			cwd:           filepath.Join(root, "Secrets", "termp"),
			wantFolding:   false,
			wantNoFolding: false,
		},
		{
			name:          "sibling sharing a string prefix with the entry",
			cwd:           filepath.Join(root, "Projects-private", "termp"),
			wantFolding:   false,
			wantNoFolding: false,
		},
		{
			name:          "sibling sharing a case-folded string prefix",
			cwd:           filepath.Join(root, "projects-private", "termp"),
			wantFolding:   false,
			wantNoFolding: false,
		},
		{
			name:          "parent of the entry",
			cwd:           root,
			wantFolding:   false,
			wantNoFolding: false,
		},
	}

	// darwin and windows fold; every other GOOS termp builds for does not.
	folding := map[string]bool{
		"darwin":  true,
		"windows": true,
		"linux":   false,
		"freebsd": false,
		"openbsd": false,
		"netbsd":  false,
	}

	resolved := allowlistTool(entry)
	for _, tt := range tests {
		for goos, folds := range folding {
			want := tt.wantNoFolding
			if folds {
				want = tt.wantFolding
			}
			t.Run(goos+"/"+tt.name, func(t *testing.T) {
				if got := resolved.directoryAllowedOn(goos, tt.cwd); got != want {
					t.Fatalf("directoryAllowedOn(%q, %q) with allowlist %q = %v, want %v", goos, tt.cwd, entry, got, want)
				}
			})
		}
	}
}

// TestDirectoryAllowlistSymlinkResolutionPerPlatform pins the second half of
// the #624 decision: darwin now resolves symlinks before comparing, exactly as
// Windows already did, because macOS keeps /tmp, /var and /etc as symlinks
// into /private, so an allowlist entry and a detected cwd routinely name the
// same directory with two different spellings. Linux and the BSDs keep their
// existing literal comparison; nothing reported a miss there and adding
// filesystem access to that comparison is not part of this fix.
func TestDirectoryAllowlistSymlinkResolutionPerPlatform(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "project"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here (%v); symlink resolution is untestable on this host", err)
	}

	// The allowlist names the symlink; the detected cwd names the real path.
	resolved := allowlistTool(link)
	cwd := filepath.Join(real, "project")
	for _, goos := range []string{"darwin", "windows"} {
		if !resolved.directoryAllowedOn(goos, cwd) {
			t.Fatalf("directoryAllowedOn(%q, %q) with allowlist %q = false, want true: the two spellings are the same directory", goos, cwd, link)
		}
	}
	for _, goos := range []string{"linux", "freebsd"} {
		if resolved.directoryAllowedOn(goos, cwd) {
			t.Fatalf("directoryAllowedOn(%q, %q) with allowlist %q = true, want false: the case-sensitive platforms still compare literally", goos, cwd, link)
		}
	}

	// A symlink pointing somewhere else must never match, on any platform:
	// resolution must not become a way for an unrelated directory to satisfy
	// an allowlist entry.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(filepath.Join(other, "project"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	elsewhere := filepath.Join(other, "project")
	for goos := range map[string]bool{"darwin": true, "windows": true, "linux": true, "freebsd": true} {
		if resolved.directoryAllowedOn(goos, elsewhere) {
			t.Fatalf("directoryAllowedOn(%q, %q) with allowlist %q = true, want false", goos, elsewhere, link)
		}
	}
}

// TestDirectoryAllowlistUnresolvableCwdFailsClosed documents the cost of
// resolving symlinks on darwin. filepath.EvalSymlinks touches the filesystem
// and fails for a path that does not exist, in which case the cleaned literal
// path is kept. When the allowlist entry resolves and the cwd does not, the
// two no longer meet and the directory is hidden. That is the fail-closed
// direction, which is the correct one for a privacy gate, and it is the
// behavior Windows has always had.
func TestDirectoryAllowlistUnresolvableCwdFailsClosed(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here (%v); symlink resolution is untestable on this host", err)
	}

	resolved := allowlistTool(link)
	gone := filepath.Join(link, "deleted-out-from-under-us")
	for _, goos := range []string{"darwin", "windows"} {
		if resolved.directoryAllowedOn(goos, gone) {
			t.Fatalf("directoryAllowedOn(%q, %q) = true, want false: an unresolvable cwd must fail closed", goos, gone)
		}
	}
}

// TestCanonicalPrivacyPathOnLeavesCaseSensitivePlatformsAlone is the direct
// unit-level guard on the fold: the canonicalization must be byte-identical to
// filepath.Clean on Linux and the BSDs, so #624's fix cannot change what those
// platforms authorize.
func TestCanonicalPrivacyPathOnLeavesCaseSensitivePlatformsAlone(t *testing.T) {
	paths := []string{
		filepath.Join("/home", "Alice", "Projects"),
		filepath.Join("/home", "alice", "projects"),
		filepath.Join("/srv", "Mixed", "..", "Case"),
	}
	for _, goos := range []string{"linux", "freebsd", "openbsd", "netbsd"} {
		for _, path := range paths {
			want := filepath.Clean(path)
			if got := canonicalPrivacyPathOn(goos, path); got != want {
				t.Fatalf("canonicalPrivacyPathOn(%q, %q) = %q, want %q", goos, path, got, want)
			}
		}
	}
}
