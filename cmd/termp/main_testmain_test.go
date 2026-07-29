package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain redirects the update cache away from the developer's real one.
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
// t.Setenv, which wins over this.
func TestMain(m *testing.M) {
	cacheHome, err := os.MkdirTemp("", "termp-test-cache")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CACHE_HOME", cacheHome); err != nil {
		panic(err)
	}
	// DefaultCachePath prefers XDG_CACHE_HOME on every platform, but belt and
	// braces for Windows: with it unset there, os.UserCacheDir reads
	// LOCALAPPDATA, so redirect that too.
	if err := os.Setenv("LOCALAPPDATA", filepath.Join(cacheHome, "local")); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(cacheHome)
	os.Exit(code)
}
