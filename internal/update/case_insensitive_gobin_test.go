package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallDetectionCaseInsensitiveGOBINMatchesRealDirectory covers #559: a
// configured GOBIN whose spelling differs from the executable's real parent
// only by case must still be recognized as the same directory on a
// case-insensitive volume (macOS default, Windows). isDirectChildOf used to
// rely on a fallback that reported "same directory" whenever either spelling
// could not be stat'ed at all, which the case-sensitive #530 fixture never
// exercised for real because both of its synthetic paths were nonexistent.
// This test creates a real directory so the comparison exercises the actual
// os.Stat/os.SameFile path instead of that removed fallback, and skips itself
// on a case-sensitive volume (the fallback's removal does not change that
// case, which TestInstallDetectionOnCaseSensitiveVolume already covers).
func TestInstallDetectionCaseInsensitiveGOBINMatchesRealDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	goBin := filepath.Join(root, "gobin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", goBin, err)
	}
	shouted := filepath.Join(root, strings.ToUpper(filepath.Base(goBin)))

	goBinInfo, err := os.Stat(goBin)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", goBin, err)
	}
	shoutedInfo, statErr := os.Stat(shouted)
	if statErr != nil || !os.SameFile(goBinInfo, shoutedInfo) {
		t.Skipf("%q is case-sensitive, so %q and %q are different directories", root, goBin, shouted)
	}

	identity := func(path string) (string, error) { return path, nil }
	executable := filepath.Join(goBin, "termp")
	for _, goos := range []string{"darwin", "windows"} {
		// The executable's real parent is goBin, but Go is configured with the
		// differently-cased GOBIN. Both spellings resolve to the same physical
		// directory on this (case-insensitive) volume, so this must still be
		// detected as a Go install.
		if got := detectInstall(executable, identity, goos, nil, shouted, "", "", nil); got != InstallGo {
			t.Fatalf("detectInstall(%q, GOBIN=%q, %q) = %q, want %q", executable, shouted, goos, got, InstallGo)
		}
	}
}

// TestIsDirectChildOfDoesNotFailOpenOnUnstattableDir is the exact #559
// repro: isDirectChildOf used to return true, meaning "same directory,"
// whenever it could not stat one of the two spellings, which is the #520
// verdict this whole comparison exists to avoid. A configured-but-not-yet-
// created GOBIN or $GOPATH/bin that only differs from the executable's real
// parent by case must be treated as unknown, not as a match.
func TestIsDirectChildOfDoesNotFailOpenOnUnstattableDir(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "no-such-dir", "gobin")
	configured := filepath.Join(root, "no-such-dir", "GOBIN")
	executable := filepath.Join(configured, "termp")

	for _, goos := range []string{"darwin", "windows"} {
		if got := isDirectChildOf(executable, realParent, goos); got {
			t.Fatalf("isDirectChildOf(%q, %q, %q) = true, want false for an unstattable, differently-cased directory", executable, realParent, goos)
		}
	}
}
