package main

import (
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestBuildActivityNonFeaturedToolNameOptOutSuppressesEverySurface(t *testing.T) {
	featured := registry.Tool{
		ID:          "featured",
		DisplayName: "Featured Tool",
		ImageKey:    "featured_key",
		ImageURL:    "https://example.test/featured.png",
	}
	secret := registry.Tool{
		ID:          "secret",
		DisplayName: "Secret Tool",
		ImageKey:    "secret_key",
		ImageURL:    "https://example.test/secret.png",
	}
	visible := registry.Tool{
		ID:          "visible",
		DisplayName: "Visible Tool",
		ImageKey:    "visible_key",
		ImageURL:    "https://example.test/visible.png",
	}
	toolNameOff := false
	cfg := config.Default()
	cfg.Tools[secret.ID] = config.ToolOverride{ToolName: &toolNameOff}

	activity := buildActivity(cfg, detector.Detection{
		Tool:   featured,
		Others: []registry.Tool{secret, visible},
	}, "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want outgoing activity")
	}
	if activity.Details != "With Visible Tool" {
		t.Fatalf("details = %q, want only the visible other tool", activity.Details)
	}
	if activity.State != "" {
		t.Fatalf("state = %q, want no collection state for the default details format", activity.State)
	}
	if activity.SmallImage.Key != visible.ImageKey {
		t.Fatalf("small image key = %q, want %q", activity.SmallImage.Key, visible.ImageKey)
	}
	if activity.SmallImage.URL != visible.ImageURL {
		t.Fatalf("small image URL = %q, want %q", activity.SmallImage.URL, visible.ImageURL)
	}
	if activity.SmallImage.Text != visible.DisplayName {
		t.Fatalf("small image text = %q, want %q", activity.SmallImage.Text, visible.DisplayName)
	}
	published := strings.Join([]string{
		activity.Details,
		activity.State,
		activity.SmallImage.Key,
		activity.SmallImage.URL,
		activity.SmallImage.Text,
	}, "\n")
	if strings.Contains(published, secret.DisplayName) {
		t.Fatalf("published other-tool surfaces contain opted-out display name %q: %q", secret.DisplayName, published)
	}
	if strings.Contains(published, "secret") {
		t.Fatalf("published other-tool surfaces contain opted-out image identity: %q", published)
	}
}

func TestBuildActivityNonFeaturedToolNameOptOutSuppressesCollectionState(t *testing.T) {
	featured := registry.Tool{ID: "featured", DisplayName: "Featured Tool"}
	secret := registry.Tool{ID: "secret", DisplayName: "Secret Tool", ImageKey: "secret_key"}
	visible := registry.Tool{ID: "visible", DisplayName: "Visible Tool", ImageKey: "visible_key"}
	toolNameOff := false
	cfg := config.Default()
	cfg.DetailsFormat = "Using {tool} now"
	cfg.Tools[secret.ID] = config.ToolOverride{ToolName: &toolNameOff}

	activity := buildActivity(cfg, detector.Detection{
		Tool:   featured,
		Others: []registry.Tool{secret, visible},
	}, "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want outgoing activity")
	}
	if activity.Details != "Using Featured Tool now" {
		t.Fatalf("details = %q, want custom featured-tool details", activity.Details)
	}
	if activity.State != "With Visible Tool" {
		t.Fatalf("state = %q, want only the visible other tool", activity.State)
	}
	if strings.Contains(activity.Details+"\n"+activity.State, secret.DisplayName) {
		t.Fatalf("details/state contain opted-out display name %q: %q / %q", secret.DisplayName, activity.Details, activity.State)
	}
}

func TestBuildActivityNonFeaturedToolWithoutOptOutKeepsDefaultDisclosure(t *testing.T) {
	featured := registry.Tool{ID: "featured", DisplayName: "Featured Tool"}
	visible := registry.Tool{
		ID:          "visible",
		DisplayName: "Visible Tool",
		ImageKey:    "visible_key",
		ImageURL:    "https://example.test/visible.png",
	}

	activity := buildActivity(config.Default(), detector.Detection{
		Tool:   featured,
		Others: []registry.Tool{visible},
	}, "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want outgoing activity")
	}
	if activity.Details != "With Visible Tool" {
		t.Fatalf("details = %q, want existing default other-tool disclosure", activity.Details)
	}
	if activity.SmallImage.Key != visible.ImageKey {
		t.Fatalf("small image key = %q, want %q", activity.SmallImage.Key, visible.ImageKey)
	}
	if activity.SmallImage.URL != visible.ImageURL {
		t.Fatalf("small image URL = %q, want %q", activity.SmallImage.URL, visible.ImageURL)
	}
	if activity.SmallImage.Text != visible.DisplayName {
		t.Fatalf("small image text = %q, want %q", activity.SmallImage.Text, visible.DisplayName)
	}
}

func TestBuildActivityFeaturedToolNameOptOutKeepsIdentitySuppressed(t *testing.T) {
	featured := registry.Tool{
		ID:          "featured",
		DisplayName: "Featured Tool",
		ImageKey:    "featured_key",
		ImageURL:    "https://example.test/featured.png",
	}
	visible := registry.Tool{ID: "visible", DisplayName: "Visible Tool", ImageKey: "visible_key"}
	toolNameOff := false
	cfg := config.Default()
	cfg.Tools[featured.ID] = config.ToolOverride{ToolName: &toolNameOff}

	activity := buildActivity(cfg, detector.Detection{
		Tool:   featured,
		Others: []registry.Tool{visible},
	}, "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want outgoing activity")
	}
	if activity.Name != presence.AppName {
		t.Fatalf("name = %q, want app name %q", activity.Name, presence.AppName)
	}
	if activity.Details != "Fixed fallback" {
		t.Fatalf("details = %q, want fallback without tool identity", activity.Details)
	}
	if activity.State != "" {
		t.Fatalf("state = %q, want no tool identity", activity.State)
	}
	if activity.LargeImage.Key != "" || activity.LargeImage.URL != "" || activity.LargeImage.Text != "" {
		t.Fatalf("large image = %+v, want no featured-tool identity", activity.LargeImage)
	}
	if activity.SmallImage.Key != "" || activity.SmallImage.URL != "" || activity.SmallImage.Text != "" {
		t.Fatalf("small image = %+v, want no other-tool identity when the featured tool name is off", activity.SmallImage)
	}
}

func TestBuildActivityOnlyOptedOutOtherLeavesCompleteFeaturedPayload(t *testing.T) {
	featured := registry.Tool{
		ID:          "featured",
		DisplayName: "Featured Tool",
		ImageKey:    "featured_key",
		ImageURL:    "https://example.test/featured.png",
	}
	secret := registry.Tool{
		ID:          "secret",
		DisplayName: "Secret Tool",
		ImageKey:    "secret_key",
		ImageURL:    "https://example.test/secret.png",
	}
	toolNameOff := false
	cfg := config.Default()
	cfg.Tools[secret.ID] = config.ToolOverride{ToolName: &toolNameOff}

	activity := buildActivity(cfg, detector.Detection{
		Tool:   featured,
		Others: []registry.Tool{secret},
	}, "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want outgoing activity")
	}
	if activity.Details != "Fixed fallback" {
		t.Fatalf("details = %q, want a complete fallback after the only other tool is suppressed", activity.Details)
	}
	if activity.State != "" {
		t.Fatalf("state = %q, want no half-populated collection", activity.State)
	}
	if activity.SmallImage.Key != "" || activity.SmallImage.URL != "" || activity.SmallImage.Text != "" {
		t.Fatalf("small image = %+v, want no half-populated image", activity.SmallImage)
	}
	if activity.Name != featured.DisplayName {
		t.Fatalf("name = %q, want featured tool name %q", activity.Name, featured.DisplayName)
	}
	if activity.LargeImage.Key != featured.ImageKey {
		t.Fatalf("large image key = %q, want %q", activity.LargeImage.Key, featured.ImageKey)
	}
	if activity.LargeImage.URL != featured.ImageURL {
		t.Fatalf("large image URL = %q, want %q", activity.LargeImage.URL, featured.ImageURL)
	}
	if activity.LargeImage.Text != featured.DisplayName {
		t.Fatalf("large image text = %q, want %q", activity.LargeImage.Text, featured.DisplayName)
	}
}
