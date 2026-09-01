package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// TestPrivacyPostureCoversDisplayPrivacyFields is the Display counterpart to
// TestPrivacyPostureCoversToolOverridePrivacyFields. Display mixes privacy
// fields (ToolName discloses tool identity throughout the activity;
// SmallImage and Collection disclose a second running tool's identity, see
// internal/presence/activity.go) with display-only fields (ElapsedTimer and
// Buttons). A new exported field must be explicitly
// classified into one of the two lists below, so it cannot silently join
// neither and inherit the #573 gap where Collection was entirely absent
// from ResolvedTool/privacyPosture and SmallImage reached ResolvedTool but
// not privacyPosture.
func TestPrivacyPostureCoversDisplayPrivacyFields(t *testing.T) {
	displayOnly := map[string]bool{
		"ElapsedTimer": true,
		"Buttons":      true,
	}
	privacyRelevant := map[string]bool{
		"ToolName":   true,
		"SmallImage": true,
		"Collection": true,
	}
	typ := reflect.TypeOf(Display{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Name
		if displayOnly[name] {
			continue
		}
		if !privacyRelevant[name] {
			t.Fatalf("Display field %q is classified as neither display-only nor privacy-relevant; "+
				"classify it in TestPrivacyPostureCoversDisplayPrivacyFields and, if privacy-relevant, "+
				"confirm postureFor/Config.Resolve cover it", name)
		}
	}
}

// TestHuntCollectionSurvivesTruncationStall is the #573 reproduction for
// display.collection: a writer truncates the config to a strict prefix that
// drops the explicit `collection = false` line, then stalls past the
// ordinary ~300ms settle budget. Before the fix, collection was entirely
// absent from privacyPosture, so the truncated (permissive default) value
// was accepted as soon as it looked stable, deep inside the loosening
// horizon, and the user's other running tools were disclosed in the details
// line.
func TestHuntCollectionSurvivesTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n" +
		"[display]\n" +
		"tool_name = true\n"
	const suffix = "collection = false\n" +
		"small_image = true\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	before, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() before truncation = %v", err)
	}
	if resolved := before.Resolve(registry.Tool{ID: "any"}); resolved.Collection {
		t.Fatal("precondition failed: collection should be off before truncation")
	}

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		if resolved := cfg.Resolve(registry.Tool{ID: "any"}); resolved.Collection {
			return fmt.Errorf("display.collection lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Display)
		}
		return nil
	})
}

// TestHuntSmallImageSurvivesTruncationStall is the #573 reproduction for
// display.small_image: same mechanism as
// TestHuntCollectionSurvivesTruncationStall, but for the setting that
// controls whether another running tool's name and icon are shown as the
// small image (internal/presence/activity.go:354).
func TestHuntSmallImageSurvivesTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n" +
		"[display]\n" +
		"tool_name = true\n"
	const suffix = "small_image = false\n" +
		"collection = true\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	before, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() before truncation = %v", err)
	}
	if resolved := before.Resolve(registry.Tool{ID: "any"}); resolved.SmallImage {
		t.Fatal("precondition failed: small_image should be off before truncation")
	}

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		if resolved := cfg.Resolve(registry.Tool{ID: "any"}); resolved.SmallImage {
			return fmt.Errorf("display.small_image lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Display)
		}
		return nil
	})
}
