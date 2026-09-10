package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	usagepkg "github.com/polter-dev/discord_terminal_presence/internal/usage"
)

var testSignalRoot string

func guardedTestSignalTermpProcessAtPath(pid int, expectedPath string, expectedStartTime uint64, startTimeKnown bool) error {
	resolvedRoot, err := filepath.EvalSymlinks(testSignalRoot)
	if err != nil {
		return fmt.Errorf("test signal guard: resolve test tree: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(expectedPath)
	if err != nil {
		return fmt.Errorf("test signal guard: resolve recorded executable path %q: %w", expectedPath, err)
	}
	if !pathWithinTree(resolvedRoot, resolvedPath) {
		return fmt.Errorf("test signal guard: recorded executable path %q lies outside test tree %q", resolvedPath, resolvedRoot)
	}
	return signalTermpProcessAtPath(pid, expectedPath, expectedStartTime, startTimeKnown)
}

func pathWithinTree(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// TestPIDFilePathStaysInsideTestTree asserts that TestMain's package-wide
// environment contains the default PID path, plus every other production
// state/config path this package's tests can reach: config.DefaultPath,
// usage.StatePath, detector.EpisodeStatePath, and daemonDiscordStatePath (the
// remaining four production readers of XDG_CONFIG_HOME/XDG_STATE_HOME
// enumerated on TestMain). On darwin the PID check fails without the
// HOME/XDG_RUNTIME_DIR redirects because os.UserCacheDir ignores
// XDG_CACHE_HOME and escapes to the ambient $HOME/Library/Caches; the four
// added checks fail — on darwin/linux — whenever the invoking shell exports
// XDG_STATE_HOME or XDG_CONFIG_HOME and TestMain does not override them,
// which is exactly the state this test tree's containment assertion is
// designed to catch. Run with those two variables exported to a scratch
// directory (outside this test's tree) to exercise that failure mode against
// stock TestMain; TestMain's redirect makes it pass regardless of what the
// invoking shell exports.
//
// This test cannot exercise the Windows branch of any of those four
// functions: each calls runtime.GOOS internally rather than taking an
// injectable resolver at this call site, and runtime.GOOS is fixed to the
// host this binary was built for. The Windows-specific containment property
// (APPDATA/USERPROFILE, not XDG_STATE_HOME/XDG_CONFIG_HOME, are what those
// functions consult on that platform — see TestMain's comment, #616) is
// instead covered per-package through each function's injectable resolver
// seam: TestDefaultPathWindowsStaysWithinRedirectedTree
// (internal/config/config_test.go), TestStatePathWindowsStaysWithinRedirectedTree
// (internal/usage/usage_test.go), and
// TestEpisodeStatePathWindowsStaysWithinRedirectedTree
// (internal/detector/detector_test.go), each of which also asserts the
// containment check would have failed had APPDATA/USERPROFILE resolved
// outside the tree — i.e. had TestMain not redirected them. The two
// assertions below only confirm TestMain exported the variables at all, not
// that a Windows path resolver would honor them.
func TestPIDFilePathStaysInsideTestTree(t *testing.T) {
	testRoot := os.Getenv("XDG_CACHE_HOME")
	if !pathWithinTree(testRoot, os.Getenv("HOME")) {
		t.Fatalf("TestMain HOME %q lies outside test tree %q", os.Getenv("HOME"), testRoot)
	}
	path := pidFilePath()
	t.Logf("pidFilePath() = %s (TestMain tree = %s)", path, testRoot)
	if !pathWithinTree(testRoot, path) {
		t.Fatalf("pidFilePath() = %q, want a path inside TestMain tree %q", path, testRoot)
	}

	paths := map[string]string{
		"config.DefaultPath()":        config.DefaultPath(),
		"usage.StatePath()":           usagepkg.StatePath(),
		"detector.EpisodeStatePath()": detector.EpisodeStatePath(),
		"daemonDiscordStatePath()":    daemonDiscordStatePath(),
	}
	for name, p := range paths {
		t.Logf("%s = %s (TestMain tree = %s)", name, p, testRoot)
		if !pathWithinTree(testRoot, p) {
			t.Fatalf("%s = %q, want a path inside TestMain tree %q — XDG_STATE_HOME/XDG_CONFIG_HOME leaked past TestMain's redirect", name, p, testRoot)
		}
	}

	// Smoke check only (see the doc comment): confirms TestMain exported
	// both Windows-native variables into the tree, not that a Windows
	// resolver honors them — that's the resolver-seam tests' job.
	for _, name := range []string{"APPDATA", "USERPROFILE"} {
		v := os.Getenv(name)
		if !pathWithinTree(testRoot, v) {
			t.Fatalf("TestMain %s %q lies outside test tree %q", name, v, testRoot)
		}
	}
}

// TestCommandSignalGuardRejectsExecutableOutsideTestTree asserts that command
// stop paths fail before reaching an OS signal when a PID record names an
// executable outside TestMain's tree.
func TestCommandSignalGuardRejectsExecutableOutsideTestTree(t *testing.T) {
	outsidePath := filepath.Join(t.TempDir(), "termp-outside-test-tree")
	if err := os.WriteFile(outsidePath, []byte("not an executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := commandSignalTermpProcessAtPath(-1, outsidePath, 0, false)
	if err == nil || !strings.Contains(err.Error(), "outside test tree") {
		t.Fatalf("command signal guard error = %v, want outside-test-tree refusal before platform signaling", err)
	}
}
