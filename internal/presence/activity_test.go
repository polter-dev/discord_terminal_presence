package presence

import (
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestActivityFromDetectionDefaultOptions(t *testing.T) {
	startedAt := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	detection := detector.Detection{
		Featured: detector.FeaturedTool{
			Tool: registry.Tool{
				ID:          "claude-code",
				DisplayName: "Claude Code",
				ImageKey:    "claude-code",
				Buttons: []registry.Button{
					{Label: "One", URL: "https://example.com/one"},
					{Label: "Two", URL: "https://example.com/two"},
					{Label: "Three", URL: "https://example.com/three"},
				},
			},
			Cwd:       "/Users/marcus/private-project",
			StartedAt: startedAt,
		},
		Tool: registry.Tool{
			ID:          "claude-code",
			DisplayName: "Claude Code",
			ImageKey:    "claude-code",
			Buttons: []registry.Button{
				{Label: "One", URL: "https://example.com/one"},
				{Label: "Two", URL: "https://example.com/two"},
				{Label: "Three", URL: "https://example.com/three"},
			},
		},
		Cwd:       "/Users/marcus/private-project",
		StartedAt: startedAt,
		Others: []registry.Tool{
			{ID: "lazygit", DisplayName: "lazygit", ImageKey: "lazygit"},
			{ID: "nvim", DisplayName: "Neovim", ImageKey: "nvim"},
		},
	}

	activity, ok := ActivityFromDetection(detection, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Name != "Claude Code" {
		t.Fatalf("name = %q, want featured tool display name", activity.Name)
	}
	if activity.Details != "Using Claude Code" {
		t.Fatalf("details = %q, want %q", activity.Details, "Using Claude Code")
	}
	if activity.State != "also: lazygit · Neovim" {
		t.Fatalf("state = %q, want collection summary", activity.State)
	}
	if activity.LargeImage.Key != "claude-code" || activity.LargeImage.URL != "" || activity.LargeImage.Text != "Claude Code" {
		t.Fatalf("large image = %#v, want key claude-code with display text", activity.LargeImage)
	}
	if activity.SmallImage.Key != "lazygit" || activity.SmallImage.Text != "lazygit" {
		t.Fatalf("small image = %#v, want top other tool", activity.SmallImage)
	}
	if activity.StartTimestamp == nil || !activity.StartTimestamp.Equal(startedAt) {
		t.Fatalf("start timestamp = %v, want %v", activity.StartTimestamp, startedAt)
	}
	if len(activity.Buttons) != 2 {
		t.Fatalf("buttons len = %d, want 2", len(activity.Buttons))
	}
	if activity.Buttons[0] != (Button{Label: "One", URL: "https://example.com/one"}) {
		t.Fatalf("button[0] = %#v", activity.Buttons[0])
	}
	if activity.Buttons[1] != (Button{Label: "Two", URL: "https://example.com/two"}) {
		t.Fatalf("button[1] = %#v", activity.Buttons[1])
	}
}

func TestActivityFromDetectionDetailsFormat(t *testing.T) {
	options := DefaultDisplayOptions()
	options.DetailsFormat = "{tool} @ {dir}"
	options.ShowDirectory = true
	detection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Codex CLI"},
		Cwd:  "/Users/marcus/work/termp",
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "Codex CLI @ termp" {
		t.Fatalf("details = %q, want custom format with directory", activity.Details)
	}

	options.ToolName = false
	activity, ok = ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "" {
		t.Fatalf("details = %q, want empty when tool_name is false", activity.Details)
	}
}

func TestActivityFromDetectionBlankDetailsFormatRendersEmpty(t *testing.T) {
	options := DefaultDisplayOptions()
	options.DetailsFormat = "   "
	detection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "" {
		t.Fatalf("details = %q, want empty", activity.Details)
	}
}

func TestActivityFromDetectionCollectionCanBeDisabledAndCapsList(t *testing.T) {
	detection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code", ImageKey: "claude-code"},
		Others: []registry.Tool{
			{DisplayName: "one"},
			{DisplayName: "two"},
			{DisplayName: "three"},
			{DisplayName: "four"},
		},
	}

	activity, ok := ActivityFromDetection(detection, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.State != "also: one · two · three" {
		t.Fatalf("state = %q, want capped collection", activity.State)
	}

	options := DefaultDisplayOptions()
	options.Collection = false
	activity, ok = ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.State != "" {
		t.Fatalf("state = %q, want empty collection when disabled", activity.State)
	}
}

func TestActivityFromDetectionDirectoryBasename(t *testing.T) {
	options := DefaultDisplayOptions()
	options.ShowDirectory = true
	detection := detector.Detection{
		Tool: registry.Tool{
			DisplayName: "Gemini CLI",
			ImageURL:    "https://example.com/gemini.png",
		},
		Cwd: "/Users/marcus/work/termp",
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.State != "termp" {
		t.Fatalf("state = %q, want basename", activity.State)
	}
	if activity.LargeImage.URL != "https://example.com/gemini.png" || activity.LargeImage.Key != "" {
		t.Fatalf("large image = %#v, want image URL", activity.LargeImage)
	}
}

func TestActivityFromDetectionDirectoryFullPath(t *testing.T) {
	options := DefaultDisplayOptions()
	options.ShowDirectory = true
	options.DirectoryBasenameOnly = false
	detection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Codex CLI", ImageKey: "codex-cli"},
		Cwd:  "/Users/marcus/work/termp",
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.State != "/Users/marcus/work/termp" {
		t.Fatalf("state = %q, want full path", activity.State)
	}
}

func TestActivityFromDetectionNone(t *testing.T) {
	activity, ok := ActivityFromDetection(detector.Detection{None: true}, DefaultDisplayOptions())
	if ok {
		t.Fatalf("ok = true with activity %#v, want false", activity)
	}
}
