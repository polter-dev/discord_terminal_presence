package presence

import (
	"strings"
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

	options := DefaultDisplayOptions()
	options.FallbackMessage = "Fixed fallback"
	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Name != "Claude Code" {
		t.Fatalf("name = %q, want featured tool display name", activity.Name)
	}
	if activity.Details != "With lazygit · Neovim" {
		t.Fatalf("details = %q, want collection summary", activity.Details)
	}
	if activity.State != "" {
		t.Fatalf("state = %q, want empty without directory", activity.State)
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
		Others: []registry.Tool{
			{DisplayName: "Claude Code"},
		},
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "Codex CLI @ 📁 termp" {
		t.Fatalf("details = %q, want custom format with directory", activity.Details)
	}
	if activity.State != "📁 termp" {
		t.Fatalf("state = %q, want directory to retain prior State priority", activity.State)
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

func TestActivityFromDetectionDefaultDetailsCascade(t *testing.T) {
	tests := []struct {
		name        string
		secondaries bool
		directory   bool
		wantDetails string
		wantState   string
	}{
		{name: "secondaries and directory", secondaries: true, directory: true, wantDetails: "With Codex CLI", wantState: "📁 dev/myrepo"},
		{name: "secondaries only", secondaries: true, wantDetails: "With Codex CLI"},
		{name: "directory only", directory: true, wantDetails: "📁 dev/myrepo"},
		{name: "fallback", wantDetails: "Fixed fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := DefaultDisplayOptions()
			options.FallbackMessage = "Fixed fallback"
			options.DirectoryBasenameOnly = false
			detection := detector.Detection{
				Tool: registry.Tool{DisplayName: "Claude Code"},
			}
			if tt.secondaries {
				detection.Others = []registry.Tool{{DisplayName: "Codex CLI"}}
			}
			if tt.directory {
				options.ShowDirectory = true
				detection.Cwd = "/Users/me/dev/myrepo"
			}

			activity, ok := ActivityFromDetection(detection, options)
			if !ok {
				t.Fatal("expected active detection to produce activity")
			}
			if activity.Details != tt.wantDetails || activity.State != tt.wantState {
				t.Fatalf("details/state = %q/%q, want %q/%q", activity.Details, activity.State, tt.wantDetails, tt.wantState)
			}
		})
	}
}

func TestActivityFromDetectionDefaultDetailsShowsDirectoryWithoutToolName(t *testing.T) {
	options := DefaultDisplayOptions()
	options.ToolName = false
	options.ShowDirectory = true
	detection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
		Cwd:  "/Users/me/dev/myrepo",
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "📁 myrepo" || activity.State != "" {
		t.Fatalf("details/state = %q/%q, want opted-in directory/empty", activity.Details, activity.State)
	}
}

func TestActivityFromDetectionBoundsRenderedText(t *testing.T) {
	detailsDetection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
		Others: []registry.Tool{
			{DisplayName: strings.Repeat("界", registry.MaxDisplayNameLength)},
		},
	}

	activity, ok := ActivityFromDetection(detailsDetection, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if got := len([]rune(activity.Details)); got != maxActivityTextLength {
		t.Fatalf("details rune count = %d, want %d", got, maxActivityTextLength)
	}
	if !strings.HasSuffix(activity.Details, "…") {
		t.Fatalf("details = %q, want graceful ellipsis", activity.Details)
	}
	if err := validateActivity(activity); err != nil {
		t.Fatalf("rendered activity failed validation: %v", err)
	}

	options := DefaultDisplayOptions()
	options.ToolName = false
	options.DetailsFormat = "{tool}"
	options.ShowDirectory = true
	stateDetection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
		Cwd:  "/" + strings.Repeat("界", registry.MaxDisplayNameLength),
	}

	activity, ok = ActivityFromDetection(stateDetection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if got := len([]rune(activity.State)); got != maxActivityTextLength {
		t.Fatalf("state rune count = %d, want %d", got, maxActivityTextLength)
	}
	if !strings.HasSuffix(activity.State, "…") {
		t.Fatalf("state = %q, want graceful ellipsis", activity.State)
	}
	if err := validateActivity(activity); err != nil {
		t.Fatalf("rendered activity failed validation: %v", err)
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
	if activity.Details != "With one · two · three" {
		t.Fatalf("details = %q, want capped collection", activity.Details)
	}

	options := DefaultDisplayOptions()
	options.Collection = false
	options.FallbackMessage = "Fixed fallback"
	activity, ok = ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Details != "Fixed fallback" || activity.State != "" {
		t.Fatalf("details/state = %q/%q, want fallback and empty state", activity.Details, activity.State)
	}
}

func TestActivityFromDetectionDirectoryRenderingPrivacyCap(t *testing.T) {
	tests := []struct {
		name         string
		cwd          string
		basenameOnly bool
		want         string
	}{
		{name: "basename only", cwd: "/Users/marcus/work/termp", basenameOnly: true, want: "📁 termp"},
		{name: "last two segments", cwd: "/Users/marcus/work/termp", want: "📁 work/termp"},
		{name: "single segment", cwd: "termp", want: "📁 termp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := DefaultDisplayOptions()
			options.ShowDirectory = true
			options.DirectoryBasenameOnly = tt.basenameOnly
			detection := detector.Detection{
				Tool: registry.Tool{DisplayName: "Gemini CLI", ImageURL: "https://example.com/gemini.png"},
				Cwd:  tt.cwd,
			}

			activity, ok := ActivityFromDetection(detection, options)
			if !ok {
				t.Fatal("expected active detection to produce activity")
			}
			if activity.Details != tt.want || activity.State != "" {
				t.Fatalf("details/state = %q/%q, want %q/empty", activity.Details, activity.State, tt.want)
			}
			if strings.Contains(activity.Details, "/Users/marcus") {
				t.Fatalf("details leaked private absolute path: %q", activity.Details)
			}
		})
	}
}

func TestActivityFromDetectionNone(t *testing.T) {
	activity, ok := ActivityFromDetection(detector.Detection{None: true}, DefaultDisplayOptions())
	if ok {
		t.Fatalf("ok = true with activity %#v, want false", activity)
	}
}
