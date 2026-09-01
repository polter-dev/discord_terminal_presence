package config

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// TestHuntToolNameSurvivesTruncationStall is the #591 reproduction for
// display.tool_name: same mechanism as
// TestHuntCollectionSurvivesTruncationStall, but for the setting that
// controls whether tool identity appears anywhere in the published activity.
func TestHuntToolNameSurvivesTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n" +
		"[display]\n" +
		"collection = true\n"
	const suffix = "tool_name = false\n" +
		"small_image = true\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	before, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() before truncation = %v", err)
	}
	if resolved := before.Resolve(registry.Tool{ID: "any"}); resolved.ToolName {
		t.Fatal("precondition failed: tool_name should be off before truncation")
	}

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		if resolved := cfg.Resolve(registry.Tool{ID: "any"}); resolved.ToolName {
			return fmt.Errorf("display.tool_name lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Display)
		}
		return nil
	})
}
