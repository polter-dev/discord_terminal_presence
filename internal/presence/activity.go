package presence

import (
	"path/filepath"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// DefaultAppID is termpresence's public Discord Application ID. An application ID is
// public (it is sent to every Discord client to render presence) and safe to embed and
// commit — it is not a secret and requires no bot token.
const DefaultAppID = "1523168764793847918"

// DisplayOptions is the M2 stand-in for future config-driven display/privacy settings.
type DisplayOptions struct {
	ToolName              bool
	ElapsedTimer          bool
	SmallImage            bool
	Buttons               bool
	ShowDirectory         bool
	DirectoryBasenameOnly bool
}

// DefaultDisplayOptions returns privacy-first defaults: all fields enabled except cwd.
func DefaultDisplayOptions() DisplayOptions {
	return DisplayOptions{
		ToolName:              true,
		ElapsedTimer:          true,
		SmallImage:            true,
		Buttons:               true,
		ShowDirectory:         false,
		DirectoryBasenameOnly: true,
	}
}

// Activity captures the Discord Rich Presence activity fields termpresence uses.
type Activity struct {
	Details        string
	State          string
	LargeImage     Image
	SmallImage     Image
	StartTimestamp *time.Time
	Buttons        []Button
}

// Image identifies either an uploaded Discord asset key or an external image URL.
type Image struct {
	Key  string
	URL  string
	Text string
}

// Button is one Discord Rich Presence button.
type Button struct {
	Label string
	URL   string
}

// ActivityFromDetection maps an active detector result into a Discord activity payload.
func ActivityFromDetection(detection detector.Detection, options DisplayOptions) (Activity, bool) {
	if detection.None {
		return Activity{}, false
	}

	tool := detection.Tool
	activity := Activity{
		LargeImage: Image{
			Key:  tool.ImageKey,
			URL:  tool.ImageURL,
			Text: tool.DisplayName,
		},
	}

	if options.ToolName {
		activity.Details = "Using " + tool.DisplayName
	}
	if options.ShowDirectory && detection.Cwd != "" {
		activity.State = directoryState(detection.Cwd, options.DirectoryBasenameOnly)
	}
	if options.ElapsedTimer && !detection.StartedAt.IsZero() {
		startedAt := detection.StartedAt
		activity.StartTimestamp = &startedAt
	}
	if options.Buttons {
		activity.Buttons = buttonsFromTool(tool)
	}

	return activity, true
}

func directoryState(cwd string, basenameOnly bool) string {
	if !basenameOnly {
		return cwd
	}
	if base := filepath.Base(cwd); base != "." && base != string(filepath.Separator) {
		return base
	}
	return cwd
}

func buttonsFromTool(tool registry.Tool) []Button {
	count := len(tool.Buttons)
	if count > 2 {
		count = 2
	}

	buttons := make([]Button, 0, count)
	for _, button := range tool.Buttons[:count] {
		buttons = append(buttons, Button{
			Label: button.Label,
			URL:   button.URL,
		})
	}
	return buttons
}
