package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain redirects user cache and runtime paths away from the developer's
// real ones.
//
// runAutomaticUpdate reaches the state file through updatepkg.DefaultCachePath
// rather than an injected path, so any test that drives it — the periodic-loop
// tests among them — read and *wrote* whatever cache the ambient environment
// pointed at. Probed on this branch before the redirect: a seeded
// ~/.cache/termp/update-check.json holding a recorded attempt for 9.9.9 came
// out of `go test ./cmd/termp` with the attempt deleted. No network call was
// involved; the damage was local state.
//
// Tests that need a specific cache location still override XDG_CACHE_HOME with
// t.Setenv, which wins over this. HOME and XDG_RUNTIME_DIR must be redirected
// too: on darwin, os.UserCacheDir ignores XDG_CACHE_HOME and resolves beneath
// $HOME/Library/Caches, and pidFilePath does not go through
// updatepkg.DefaultCachePath. Without those redirects, stop-path tests can read
// and signal the developer's real daemon (issue #590).
//
// XDG_STATE_HOME and XDG_CONFIG_HOME get the same treatment for the same
// reason, closing the part of that defect class #590 left open:
// config.DefaultPath (internal/config/config.go), usage.StatePath
// (internal/usage/usage.go), detector.EpisodeStatePath
// (internal/detector/episode.go), and daemonDiscordStatePath (derived from
// usage's state directory, cmd/termp/main.go) all honor an exported
// XDG_STATE_HOME or XDG_CONFIG_HOME ahead of HOME. Redirecting only HOME, as
// above, is not enough: any developer or CI runner with either variable set
// in their ambient shell would otherwise have `go test ./cmd/termp` read and
// write their real config.toml, usage.json, presence.json, and discord.json.
// It is dormant on a machine that happens not to export them, which is
// exactly how the #590 fix still missed it. os.Setenv unconditionally
// overwrites whatever the ambient shell exported, so a hostile inherited
// value is replaced rather than merely shadowed. See
// TestPIDFilePathStaysInsideTestTree, which now also asserts these paths.
func TestMain(m *testing.M) {
	testRoot, err := os.MkdirTemp("", "termp-test-cache")
	if err != nil {
		panic(err)
	}
	home := filepath.Join(testRoot, "home")
	runtimeDir := filepath.Join(testRoot, "run")
	localAppData := filepath.Join(testRoot, "local")
	xdgConfigHome := filepath.Join(testRoot, "xdg-config")
	xdgStateHome := filepath.Join(testRoot, "xdg-state")
	for _, dir := range []string{
		home,
		filepath.Join(home, "Library", "Caches"),
		runtimeDir,
		localAppData,
		xdgConfigHome,
		xdgStateHome,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			panic(err)
		}
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CACHE_HOME", testRoot); err != nil {
		panic(err)
	}
	// DefaultCachePath prefers XDG_CACHE_HOME on every platform, but belt and
	// braces for Windows: with it unset there, os.UserCacheDir reads
	// LOCALAPPDATA, so redirect that too.
	if err := os.Setenv("LOCALAPPDATA", localAppData); err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", xdgConfigHome); err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", xdgStateHome); err != nil {
		panic(err)
	}
	testSignalRoot = testRoot
	commandSignalTermpProcessAtPath = guardedTestSignalTermpProcessAtPath
	code := m.Run()
	os.RemoveAll(testRoot)
	os.Exit(code)
}
