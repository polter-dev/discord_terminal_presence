package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// #635: a config without an explicit enabled key starts on an uncertified seed
// for the loosening horizon. These reproduce start()'s wiring and the first two
// statements of run(), which nothing in the suite can call directly.

func TestStartupUpdateCheckOptOutAppliesDuringHorizon(t *testing.T) {
	t.Setenv("NO_UPDATE_CHECK", "")
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("update_check = false\nauto_update = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, _, err := newWatchedConfigManager(configPath, func(m *config.Manager) {
		startConfigWatchWithRetry(ctx, m, configPath)
	})
	if err != nil {
		t.Fatalf("startup config error = %v", err)
	}
	defer manager.Close()

	checker := newSignallingChecker("")
	// interval 0 runs only the startup refresh, as the daemon does first.
	runPeriodicAutomaticUpdate(ctx, manager.Current, "1.0.0", checker, &recordingUpdateRunner{}, 0)
	if got := checker.count(); got != 0 {
		t.Fatalf("daemon startup performed %d release check(s) although the saved config says update_check = false", got)
	}
}

func TestStartupFallbackMessagesApplyDuringHorizon(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("fallback_messages = [\"Shipping things\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, _, err := newWatchedConfigManager(configPath, func(m *config.Manager) {
		startConfigWatchWithRetry(ctx, m, configPath)
	})
	if err != nil {
		t.Fatalf("startup config error = %v", err)
	}
	defer manager.Close()

	// run(): cfg, initialConfigErr := manager.Current()
	//        fallbackMessage := selectFallbackMessage(cfg.FallbackMessages)
	cfg, _ := manager.Current()
	fallbackMessage := selectFallbackMessage(cfg.FallbackMessages)

	// The real config arrives as a hot reload once the loosening horizon passes.
	var reloaded config.Config
	select {
	case result := <-manager.Reloads():
		if result.Err != nil {
			t.Fatalf("reload error = %v", result.Err)
		}
		reloaded = result.Config
	case <-time.After(10 * time.Second):
		t.Fatal("no reload arrived")
	}
	if !reloaded.Enabled || !slices.Equal(reloaded.FallbackMessages, []string{"Shipping things"}) {
		t.Fatalf("precondition: reload should carry the saved config, got %+v", reloaded.FallbackMessages)
	}

	// run() keeps using the fallbackMessage it picked at startup.
	detection := detector.Detection{Tool: registry.Tool{ID: "claude", DisplayName: "Claude Code"}}
	detection.Featured.Tool = detection.Tool
	activity := buildActivity(reloaded, detection, fallbackMessage)
	if activity == nil {
		t.Fatal("no activity")
	}
	if activity.Details != "Shipping things" {
		t.Fatalf("published details = %q; the only configured fallback message is %q", activity.Details, "Shipping things")
	}
}
