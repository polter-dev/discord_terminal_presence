package presence

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestActivityFromDetectionCollectionDoesNotExposeToolNamesWhenToolNameDisabled(t *testing.T) {
	options := DefaultDisplayOptions()
	options.ToolName = false
	options.Collection = true
	options.FallbackMessage = "Fixed fallback"
	detection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
		Others: []registry.Tool{
			{DisplayName: "Aider"},
		},
	}

	activity, ok := ActivityFromDetection(detection, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if strings.Contains(activity.Details, "Aider") || strings.Contains(activity.State, "Aider") {
		t.Fatalf("details/state = %q/%q, must not expose another tool name when tool_name is false", activity.Details, activity.State)
	}
	if activity.Details != "Fixed fallback" || activity.State != "" {
		t.Fatalf("details/state = %q/%q, want fallback and empty state", activity.Details, activity.State)
	}
}

func TestActivityFromDetectionBoundsRenderedText(t *testing.T) {
	detailsDetection := detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
		Others: []registry.Tool{
			{DisplayName: strings.Repeat("界", registry.MaxDisplayNameLength)},
		},
	}

	// #445: the mapping layer no longer bounds anything itself — that
	// guarantee now lives solely in normalizeActivity, the choke point. Only
	// after normalizing is the result guaranteed bounded and valid.
	activity, ok := ActivityFromDetection(detailsDetection, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	activity = normalizeActivity(activity)
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
	activity = normalizeActivity(activity)
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

func TestActivityFromDetectionOmitsTooShortRenderedTextWithoutGlobalLogging(t *testing.T) {
	originalLogOutput := log.Writer()
	t.Cleanup(func() {
		log.SetOutput(originalLogOutput)
	})
	var logs bytes.Buffer
	log.SetOutput(&logs)

	options := DefaultDisplayOptions()
	options.DetailsFormat = "x"
	activity, ok := ActivityFromDetection(detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
	}, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	// #445: the mapping layer no longer truncates/drops before
	// sanitization — it only reports the omission diagnostic below. The raw
	// pre-sanitize value survives until normalizeActivity (the choke point)
	// sanitizes and bounds it for real.
	if activity.Details != "x" {
		t.Fatalf("details = %q, want raw pre-sanitize text (dropping happens at the choke point)", activity.Details)
	}
	if logs.Len() != 0 {
		t.Fatalf("global log output = %q, want none", logs.String())
	}

	_, ok, omissions := ActivityFromDetectionWithOmissions(detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
	}, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if len(omissions) != 1 || omissions[0] != (ActivityTextOmission{Field: "details", Length: 1, Minimum: 2}) {
		t.Fatalf("omissions = %#v, want details omission", omissions)
	}

	// The choke point still enforces the bound end-to-end: once normalized
	// (as SetActivity would do), the too-short field is dropped for real.
	normalized := normalizeActivity(activity)
	if normalized.Details != "" {
		t.Fatalf("normalized details = %q, want dropped at the choke point", normalized.Details)
	}
}

func TestActivityFromDetectionOmitsTooShortImageTooltips(t *testing.T) {
	options := DefaultDisplayOptions()
	activity, ok := ActivityFromDetection(detector.Detection{
		Tool: registry.Tool{DisplayName: "x", ImageKey: "featured"},
		Others: []registry.Tool{
			{DisplayName: "界", ImageKey: "other"},
		},
	}, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if activity.Name != "x" {
		t.Fatalf("name = %q, want one-character name preserved", activity.Name)
	}
	// #445: the mapping layer no longer drops before sanitization; the
	// choke point (normalizeActivity) is what actually enforces the bound.
	if activity.LargeImage.Text != "x" {
		t.Fatalf("large image text = %q, want raw pre-sanitize tooltip preserved", activity.LargeImage.Text)
	}
	if activity.SmallImage.Text != "界" {
		t.Fatalf("small image text = %q, want raw pre-sanitize tooltip preserved", activity.SmallImage.Text)
	}

	normalized := normalizeActivity(activity)
	if normalized.LargeImage.Text != "" {
		t.Fatalf("normalized large image text = %q, want dropped at the choke point", normalized.LargeImage.Text)
	}
	if normalized.SmallImage.Text != "" {
		t.Fatalf("normalized small image text = %q, want dropped at the choke point", normalized.SmallImage.Text)
	}
}

// TestActivityFromDetectionDoesNotPreTruncateBeforeSanitization is the
// regression test for #445. A 200-rune directory basename made of "x"+BEL
// pairs exceeds the 128-rune Discord bound before sanitization, but
// Sanitize strips every BEL, leaving only 100 "x" runes plus the "📁 "
// prefix (102 runes) — comfortably under the bound. The mapping layer must
// not truncate the raw value before normalizeActivity (the choke point) has
// a chance to sanitize it, or it destroys content the choke point would
// have kept and can add a false trailing ellipsis.
func TestActivityFromDetectionDoesNotPreTruncateBeforeSanitization(t *testing.T) {
	basename := strings.Repeat("x\x07", 100)
	options := DefaultDisplayOptions()
	options.ShowDirectory = true
	options.DirectoryBasenameOnly = true

	activity, ok, omissions := ActivityFromDetectionWithOmissions(detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
		Cwd:  "/parent/" + basename,
	}, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if len(omissions) != 0 {
		t.Fatalf("omissions = %#v, want none: the sanitized details comfortably clear the minimum", omissions)
	}

	normalized := normalizeActivity(activity)
	wantDetails := "📁 " + strings.Repeat("x", 100)
	if normalized.Details != wantDetails {
		t.Fatalf("normalized details = %q (%d runes), want %q (%d runes) with the full sanitized content and no ellipsis",
			normalized.Details, utf8.RuneCountInString(normalized.Details),
			wantDetails, utf8.RuneCountInString(wantDetails))
	}
	if strings.Contains(normalized.Details, "…") {
		t.Fatalf("normalized details = %q, want no trailing ellipsis", normalized.Details)
	}
}

// TestActivityFromDetectionOmissionDiagnosticsAgreeWithChokePoint is the
// diagnostic-truthfulness half of #445: a field the choke point actually
// drops (because it sanitizes below the 2-rune minimum) must be reported as
// an omission, and a field the choke point keeps must not be — the two
// layers must never disagree about what "omitted" means.
func TestActivityFromDetectionOmissionDiagnosticsAgreeWithChokePoint(t *testing.T) {
	options := DefaultDisplayOptions()
	// Raw length is 3 (>= the minimum), so a pre-sanitize length check would
	// not flag this as too short; sanitizing strips both BEL characters,
	// leaving just "x" (length 1), which the choke point drops. This is
	// exactly the latent case #445 called out: the pre-#445 mapping layer
	// checked the raw length and reported no omission here at all, silently
	// disagreeing with the choke point actually dropping the field.
	options.DetailsFormat = "x\x07\x07"
	activity, ok, omissions := ActivityFromDetectionWithOmissions(detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
	}, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if len(omissions) != 1 || omissions[0].Field != "details" {
		t.Fatalf("omissions = %#v, want a details omission (choke point drops it)", omissions)
	}
	normalized := normalizeActivity(activity)
	if normalized.Details != "" {
		t.Fatalf("normalized details = %q, want dropped at the choke point to agree with the reported omission", normalized.Details)
	}

	// A field the choke point keeps must not be reported.
	options.DetailsFormat = "ok"
	activity, ok, omissions = ActivityFromDetectionWithOmissions(detector.Detection{
		Tool: registry.Tool{DisplayName: "Claude Code"},
	}, options)
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}
	if len(omissions) != 0 {
		t.Fatalf("omissions = %#v, want none: the choke point keeps this field", omissions)
	}
	normalized = normalizeActivity(activity)
	if normalized.Details == "" {
		t.Fatal("normalized details = \"\", want the choke point to keep this field, agreeing with the absence of an omission")
	}
}

func TestValidateActivityEnforcesPerFieldMinimums(t *testing.T) {
	for _, tt := range []struct {
		name     string
		activity Activity
	}{
		{name: "details", activity: Activity{Details: "x"}},
		{name: "state", activity: Activity{State: "界"}},
		{name: "large image text", activity: Activity{LargeImage: Image{Text: "x"}}},
		{name: "small image text", activity: Activity{SmallImage: Image{Text: "界"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateActivity(tt.activity)
			if err == nil || !strings.Contains(err.Error(), "must be at least 2 characters") {
				t.Fatalf("validateActivity() error = %v, want minimum-length error", err)
			}
		})
	}

	if err := validateActivity(Activity{Name: "x"}); err != nil {
		t.Fatalf("validateActivity() rejected one-character name: %v", err)
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
