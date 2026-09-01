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
func TestMain(m *testing.M) {
	testRoot, err := os.MkdirTemp("", "termp-test-cache")
	if err != nil {
		panic(err)
	}
	home := filepath.Join(testRoot, "home")
	runtimeDir := filepath.Join(testRoot, "run")
	localAppData := filepath.Join(testRoot, "local")
	for _, dir := range []string{
		home,
		filepath.Join(home, "Library", "Caches"),
		runtimeDir,
		localAppData,
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
	testSignalRoot = testRoot
	commandSignalTermpProcessAtPath = guardedTestSignalTermpProcessAtPath
	code := m.Run()
	os.RemoveAll(testRoot)
	os.Exit(code)
}
