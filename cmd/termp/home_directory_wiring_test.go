package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// TestHomeDirectoryWiringRendersTildeNotAccountName drives the real
// production wiring for #620 end to end, through t.Setenv("HOME", ...) rather
// than a hardcoded `home` string. t.Setenv wins over TestMain's package-wide
// HOME redirect (main_testmain_test.go) for the duration of this test, which
// is exactly what's needed here.
//
// Every other test covering buildActivity/debugDetectionDirectory's directory
// handling (TestBuildActivityDirectoryPrivacy,
// TestDebugDetectionDirectoryHonorsPrivacy, and presence's own
// TestDirectoryDisplayHomeRedaction) passes `home` as a plain argument or
// bakes a fake "home-looking" name into a cwd string; none of them ever call
// resolveHomeDir() or os.UserHomeDir(). A refactor that dropped `Home:
// resolveHomeDir()` from the DisplayOptions literal in buildActivity, or that
// stopped calling resolveHomeDir() in debugDetectionDirectory, would pass
// every one of those tests while silently reintroducing the account-name leak
// #620 fixed. This test would not: it fails without the wiring (verified by
// hand while preparing this change; see the commit message and hand-back for
// the captured failure output).
func TestHomeDirectoryWiringRendersTildeNotAccountName(t *testing.T) {
	const accountName = "wiring-test-account-620"
	home := filepath.Join(t.TempDir(), accountName)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	tool := registry.Tool{ID: "test-tool", DisplayName: "Test Tool"}
	cfg := config.Default()
	cfg.DetailsFormat = "{tool}"
	cfg.Privacy.ShowDirectory = true
	cfg.Privacy.DirectoryBasenameOnly = true

	t.Run("buildActivity cwd exactly home", func(t *testing.T) {
		activity := buildActivity(cfg, detector.Detection{
			Tool: tool,
			Cwd:  home,
		}, "fallback")
		if activity == nil {
			t.Fatal("activity = nil, want outgoing activity")
		}
		if activity.State != "📁 ~" {
			t.Fatalf("activity.State = %q, want %q", activity.State, "📁 ~")
		}
		if strings.Contains(activity.State, accountName) {
			t.Fatalf("activity.State leaked the account name: %q", activity.State)
		}
	})

	t.Run("buildActivity project directly under home, two-segment mode", func(t *testing.T) {
		twoSeg := cfg
		twoSeg.Privacy.DirectoryBasenameOnly = false
		project := filepath.Join(home, "myproject")
		activity := buildActivity(twoSeg, detector.Detection{
			Tool: tool,
			Cwd:  project,
		}, "fallback")
		if activity == nil {
			t.Fatal("activity = nil, want outgoing activity")
		}
		if activity.State != "📁 ~/myproject" {
			t.Fatalf("activity.State = %q, want %q", activity.State, "📁 ~/myproject")
		}
		if strings.Contains(activity.State, accountName) {
			t.Fatalf("activity.State leaked the account name: %q", activity.State)
		}
	})

	t.Run("debugDetectionDirectory cwd exactly home", func(t *testing.T) {
		got := debugDetectionDirectory(cfg, detector.Detection{
			Tool: tool,
			Cwd:  home,
		})
		if got != "~" {
			t.Fatalf("debugDetectionDirectory(...) = %q, want %q", got, "~")
		}
		if strings.Contains(got, accountName) {
			t.Fatalf("debugDetectionDirectory leaked the account name: %q", got)
		}
	})
}
