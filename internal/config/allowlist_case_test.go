package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostPath rewrites a slash-shaped test path into one shaped like the HOST's
// filesystem. Tables below stay readable as "/a/Projects" while every value
// that actually reaches a function under test is separator-correct on whatever
// runner executes it.
//
// This helper exists because of a rule that this file has now violated three
// times, each time going red only on windows-latest: only CASE SENSITIVITY is
// injected from goos. Separators, volume parsing and every structural behavior
// of filepath.Rel still follow the host, and pathHasPrefixOn's doc comment says
// so explicitly -- "Callers must pass host-shaped paths."
//
// A bare "/a/Projects" literal breaks that rule on a Windows host three ways at
// once: filepath.Clean rewrites it to `\a\Projects`, filepath.Join builds
// `\a\Projects\termp`, and neither is ever equal to the forward-slash literal
// the test compares against. The result is a test that asserts something
// different on Windows than it does on Unix -- so it fails there for a reason
// that has nothing to do with the behavior under test. Route every path literal
// in this file through hostPath (or build it with filepath.Join/t.TempDir) so
// all three hosts assert the same thing.
func hostPath(path string) string {
	return filepath.FromSlash(path)
}

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
// test is the case fold. The paths are host-shaped because only case rules
// follow goos; separators and volume parsing still follow the host. See
// directoryAllowedOn's doc comment for exactly where that line falls.
//
// This test failed on windows-latest before #630 completed the seam: goos was
// injected into canonicalization but not into the prefix comparison, so
// filepath.Rel folded case on the Windows runner even for goos "linux" and
// the four non-folding platforms all reported a match.
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
//
// The inputs deliberately arrive uncleaned (the ".." row) so the assertion
// covers the whole of canonicalPrivacyPathOn's non-folding branch rather than
// just its return value for an already-normalized path.
func TestCanonicalPrivacyPathOnLeavesCaseSensitivePlatformsAlone(t *testing.T) {
	paths := []string{
		hostPath("/home/Alice/Projects"),
		hostPath("/home/alice/projects"),
		hostPath("/srv/Mixed/../Case"),
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

// TestPathHasPrefixOnUsesInjectedCaseRules is the direct unit-level guard on
// the half of the seam #630 added: the prefix comparison itself must follow
// the injected platform, not the host.
//
// The operands here are deliberately NOT pre-canonicalized, unlike at the
// directoryAllowedOn call site. That is the point: pathHasPrefixOn must reach
// the injected platform's answer on its own rather than inheriting it from
// whatever the caller happened to do first, because a helper that is only
// correct when its caller has already lowercased both arguments is the same
// half-working abstraction that produced this bug.
//
// "Not pre-canonicalized" means not pre-folded and not pre-Cleaned; it does
// NOT mean POSIX-shaped. The table is written with slashes for legibility and
// converted to host shape by hostPath at the call, because separators follow
// the host on every row regardless of tt.goos.
func TestPathHasPrefixOnUsesInjectedCaseRules(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		path   string
		prefix string
		want   bool
	}{
		// Folding platforms: case must be ignored on ANY host. On a
		// case-sensitive host these pass only because pathHasPrefixOn
		// lowercases the operands itself.
		{"darwin descendant differing by case", "darwin", "/a/projects/termp", "/a/Projects", true},
		{"darwin equal differing by case", "darwin", "/a/PROJECTS", "/a/Projects", true},
		{"windows descendant differing by case", "windows", "/a/projects/termp", "/a/Projects", true},
		{"windows equal differing by case", "windows", "/a/PROJECTS", "/a/Projects", true},
		{"darwin unrelated sibling", "darwin", "/a/secrets/termp", "/a/Projects", false},
		{"darwin string-prefix sibling", "darwin", "/a/projects-private/x", "/a/Projects", false},

		// Non-folding platforms: case must be honored on ANY host. On a
		// Windows host these pass only because of the exact re-derivation
		// after filepath.Rel; without it Rel's EqualFold accepts all three
		// case-divergent rows.
		{"linux descendant differing by case", "linux", "/a/projects/termp", "/a/Projects", false},
		{"linux equal differing by case", "linux", "/a/PROJECTS", "/a/Projects", false},
		{"linux intermediate element differing by case", "linux", "/a/Projects/termp", "/A/Projects", false},
		{"freebsd descendant differing by case", "freebsd", "/a/projects/termp", "/a/Projects", false},

		// Exact spellings must still match everywhere, on any host: the
		// re-derivation must not reject a legitimate descendant.
		{"linux exact descendant", "linux", "/a/Projects/termp", "/a/Projects", true},
		{"linux exact deep descendant", "linux", "/a/Projects/x/y/z", "/a/Projects", true},
		{"linux exact equal", "linux", "/a/Projects", "/a/Projects", true},
		{"darwin exact descendant", "darwin", "/a/Projects/termp", "/a/Projects", true},

		// filepath.Rel's structural behavior must survive unchanged.
		{"linux parent is not a descendant", "linux", "/a", "/a/Projects", false},
		{"linux uncleaned operands are cleaned", "linux", "/a/Projects/./x/../x", "/a/b/../Projects", true},
		// The root prefix is meaningful on every host: hostPath turns "/"
		// into Windows' volume-relative root `\`, filepath.Rel accepts it
		// (both operands have an empty volume name and both are rooted), and
		// Windows' filepath.Join does not double the separator when the first
		// element already ends in one -- so Join(`\`, "foo") is `\foo`.
		{"linux root prefix", "linux", "/foo", "/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, prefix := hostPath(tt.path), hostPath(tt.prefix)
			if got := pathHasPrefixOn(tt.goos, path, prefix); got != tt.want {
				t.Fatalf("pathHasPrefixOn(%q, %q, %q) = %v, want %v", tt.goos, path, prefix, got, tt.want)
			}
		})
	}
}

// TestPathHasPrefixOnNonFoldingMatchesAreExact states the #630 invariant as a
// property rather than a table, so it holds for inputs nobody enumerated: on a
// non-folding platform, pathHasPrefixOn may only return true when prefix is
// present in path byte for byte.
//
// Honest scope: on a case-sensitive host (darwin, Linux) filepath.Rel already
// satisfies every row, so this test cannot fail here and could not have caught
// the bug here. It earns its keep on windows-latest, where filepath.Rel folds
// case and the exact re-derivation is the only thing upholding the invariant.
// A Windows-host-only defect is not observable from a darwin host; what this
// pins is the property, on whichever host runs it.
//
// Both the operands and the byte-for-byte expectation are host-shaped, which
// is what makes "exact" mean the same thing on every runner: the expectation
// already joins with filepath.Separator, so feeding it slash-shaped operands
// while pathHasPrefixOn Cleaned them to backslashes made the two halves
// disagree on Windows for reasons unrelated to case.
func TestPathHasPrefixOnNonFoldingMatchesAreExact(t *testing.T) {
	prefixes := []string{"/a/Projects", "/a/projects", "/A/Projects"}
	paths := []string{
		"/a/Projects", "/a/projects", "/a/PROJECTS",
		"/a/Projects/termp", "/a/projects/termp", "/A/Projects/termp",
		"/a/Projects-private/termp", "/a/projects-private/termp",
		"/a", "/b/Projects/termp",
	}
	for _, goos := range []string{"linux", "freebsd", "openbsd", "netbsd"} {
		for _, rawPrefix := range prefixes {
			for _, rawPath := range paths {
				prefix, path := hostPath(rawPrefix), hostPath(rawPath)
				got := pathHasPrefixOn(goos, path, prefix)
				exact := path == prefix ||
					strings.HasPrefix(path, prefix+string(filepath.Separator))
				if got && !exact {
					t.Fatalf("pathHasPrefixOn(%q, %q, %q) = true, but %q is not a byte-for-byte prefix of %q: a case-sensitive platform must never match on folded case", goos, path, prefix, prefix, path)
				}
				if exact && !got {
					t.Fatalf("pathHasPrefixOn(%q, %q, %q) = false, but %q IS a byte-for-byte prefix of %q: the exactness check must not reject a real descendant", goos, path, prefix, prefix, path)
				}
			}
		}
	}
}

// TestPathCaseFoldsOnCoversEveryBuildPlatform pins the single platform
// predicate both halves of the seam read. Splitting this decision across two
// functions is what let canonicalization and comparison disagree in #630.
func TestPathCaseFoldsOnCoversEveryBuildPlatform(t *testing.T) {
	want := map[string]bool{
		"darwin":  true,
		"windows": true,
		"linux":   false,
		"freebsd": false,
		"openbsd": false,
		"netbsd":  false,
	}

	// Detect folding by the property the seam actually depends on -- two
	// spellings that differ ONLY by case canonicalize to the same string --
	// rather than by asking whether canonicalPrivacyPathOn changed its input.
	//
	// The "changed its input" form this replaces (canonicalPrivacyPathOn(goos,
	// "/A/B") != "/A/B") conflated the case fold with filepath.Clean's own
	// rewriting, so it did not measure folding at all. On a Windows host Clean
	// turns "/A/B" into `\A\B`, which differs from the literal for EVERY goos,
	// so openbsd was reported as folding. It is latently wrong on Unix too:
	// any input Clean normalizes -- a trailing slash, a doubled separator, a
	// ".." -- would report a fold that never happened. Comparing two spellings
	// against each other has no such coupling, because Clean, EvalSymlinks and
	// the separator all act identically on both operands and cancel out.
	//
	// Both spellings live under a nonexistent subdirectory of t.TempDir(), so
	// EvalSymlinks fails identically for each and cannot introduce a
	// difference of its own, and the shared parent means the only thing that
	// can differ is the case of the two trailing elements.
	base := filepath.Join(t.TempDir(), "no-such-root")
	upper := filepath.Join(base, "A", "B")
	lower := filepath.Join(base, "a", "b")

	for goos, folds := range want {
		if got := pathCaseFoldsOn(goos); got != folds {
			t.Fatalf("pathCaseFoldsOn(%q) = %v, want %v", goos, got, folds)
		}
		// The two halves must never disagree about a platform.
		canonicalUpper := canonicalPrivacyPathOn(goos, upper)
		canonicalLower := canonicalPrivacyPathOn(goos, lower)
		canonicalFolds := canonicalUpper == canonicalLower
		if canonicalFolds != folds {
			t.Fatalf("canonicalPrivacyPathOn(%q) folded %q and %q to %q and %q (folds = %v), but pathCaseFoldsOn(%q) = %v: the seam's two halves disagree", goos, upper, lower, canonicalUpper, canonicalLower, canonicalFolds, goos, folds)
		}
	}
}

// TestRelMatchedExactlyRejectsFoldedTriples is the one test in this file that
// exercises #630's actual fix on a case-sensitive host.
//
// On darwin and Linux, filepath.Rel never folds case, so it never produces the
// (path, prefix, rel) triple that made windows-latest fail -- which means no
// test that goes through pathHasPrefixOn can observe the exactness check
// working here. (Deleting the check leaves this package's suite fully green on
// darwin; only windows-latest goes red.) So the check is called directly with
// the triples a Windows host's EqualFold-based Rel would hand it.
//
// The "rel" values below are the real ones: filepath.Rel on a Windows host
// returns targ[t0:], a verbatim tail of the target path, so a folded match
// yields the same rel as an exact one -- which is precisely why rel alone
// cannot be trusted and prefix must be re-checked.
//
// rel goes through hostPath along with path and prefix: relMatchedExactly
// rejoins rel onto prefix with filepath.Join, so a multi-element rel has to be
// spelled with the host's separator for the rejoin to reproduce path.
func TestRelMatchedExactlyRejectsFoldedTriples(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		prefix string
		rel    string
		want   bool
	}{
		// The windows-latest failure from PR #630, reduced to its triple:
		// Rel folded "projects" onto "Projects" and returned "termp".
		{"final element folded", "/a/no-such-root/projects/termp", "/a/no-such-root/Projects", "termp", false},
		{"final element upper-cased", "/a/no-such-root/PROJECTS/termp", "/a/no-such-root/Projects", "termp", false},
		{"intermediate element folded", "/A/Projects/termp", "/a/Projects", "termp", false},
		{"several elements folded", "/a/projects/sub/termp", "/a/Projects", "sub/termp", false},

		// Exact spellings must be accepted, or the check would break every
		// legitimate match on every platform.
		{"exact single element", "/a/Projects/termp", "/a/Projects", "termp", true},
		{"exact multi element", "/a/Projects/sub/termp", "/a/Projects", "sub/termp", true},
		// Kept unscoped rather than restricted to Unix: hostPath maps "/" to
		// `\`, Windows' volume-relative root, and Windows' filepath.Join
		// strips leading separators from the next element after a trailing
		// one, so Join(`\`, "foo") is `\foo` and not the UNC-looking `\\foo`.
		// The row therefore asserts the same thing on every host.
		{"exact under root", "/foo", "/", "foo", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, prefix, rel := hostPath(tt.path), hostPath(tt.prefix), hostPath(tt.rel)
			if got := relMatchedExactly(path, prefix, rel); got != tt.want {
				t.Fatalf("relMatchedExactly(%q, %q, %q) = %v, want %v", path, prefix, rel, got, tt.want)
			}
		})
	}
}

// TestRelMatchedExactlyAgreesWithRealRel ties the direct triples above back to
// reality: for every exact-spelling case, the rel filepath.Rel actually
// produces on THIS host must be one relMatchedExactly accepts. Without this,
// the test above could drift into asserting something filepath.Rel never
// returns.
//
// This is also the row-level check that the hand-written rels above are
// host-shaped: it derives rel from the host's own filepath.Rel rather than
// spelling it out, so if hostPath ever stopped matching what Rel produces,
// this test fails on that host.
func TestRelMatchedExactlyAgreesWithRealRel(t *testing.T) {
	pairs := [][2]string{
		{"/a/Projects", "/a/Projects/termp"},
		{"/a/Projects", "/a/Projects/sub/deep/termp"},
		{"/", "/foo"},
		{"/a", "/a/b"},
	}
	for _, pair := range pairs {
		prefix := filepath.Clean(hostPath(pair[0]))
		path := filepath.Clean(hostPath(pair[1]))
		rel, err := filepath.Rel(prefix, path)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) error = %v", prefix, path, err)
		}
		if !relMatchedExactly(path, prefix, rel) {
			t.Fatalf("relMatchedExactly(%q, %q, %q) = false, want true: the exactness check must accept a rel filepath.Rel really produced", path, prefix, rel)
		}
	}
}
