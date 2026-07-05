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
	}

	activity, ok := ActivityFromDetection(detection, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "Using Claude Code" {
		t.Fatalf("details = %q, want %q", activity.Details, "Using Claude Code")
	}
	if activity.State != "" {
		t.Fatalf("state = %q, want empty by default", activity.State)
	}
	if activity.LargeImage.Key != "claude-code" || activity.LargeImage.URL != "" || activity.LargeImage.Text != "Claude Code" {
		t.Fatalf("large image = %#v, want key claude-code with display text", activity.LargeImage)
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

func TestActivityFromDetectionDirectoryBasename(t *testing.T) {
	options := DefaultDisplayOptions()
	options.ShowDirectory = true
	detection := detector.Detection{
		Tool: registry.Tool{
			DisplayName: "Gemini CLI",
			ImageURL:    "https://example.com/gemini.png",
		},
		Cwd: "/Users/marcus/work/termpresence",
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.State != "termpresence" {
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
		Cwd:  "/Users/marcus/work/termpresence",
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.State != "/Users/marcus/work/termpresence" {
		t.Fatalf("state = %q, want full path", activity.State)
	}
}

func TestActivityFromDetectionNone(t *testing.T) {
	activity, ok := ActivityFromDetection(detector.Detection{None: true}, DefaultDisplayOptions())
	if ok {
		t.Fatalf("ok = true with activity %#v, want false", activity)
	}
}
