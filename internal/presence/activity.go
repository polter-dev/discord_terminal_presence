package presence

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// DefaultAppID is termp's public Discord Application ID. An application ID is
// public (it is sent to every Discord client to render presence) and safe to embed and
// commit — it is not a secret and requires no bot token.
const DefaultAppID = "1523168764793847918"

// DisplayOptions is the M2 stand-in for future config-driven display/privacy settings.
type DisplayOptions struct {
	ToolName              bool
	DetailsFormat         string
	ElapsedTimer          bool
	SmallImage            bool
	Collection            bool
	Buttons               bool
	ShowDirectory         bool
	DirectoryBasenameOnly bool
}

// DefaultDisplayOptions returns privacy-first defaults: all fields enabled except cwd.
func DefaultDisplayOptions() DisplayOptions {
	return DisplayOptions{
		ToolName:              true,
		DetailsFormat:         "Using {tool}",
		ElapsedTimer:          true,
		SmallImage:            true,
		Collection:            true,
		Buttons:               true,
		ShowDirectory:         false,
		DirectoryBasenameOnly: true,
	}
}

// Activity captures the Discord Rich Presence activity fields termp uses.
type Activity struct {
	AppID          string
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

	tool := detection.Featured.Tool
	if tool.ID == "" {
		tool = detection.Tool
	}
	activity := Activity{
		AppID: tool.AppID,
		LargeImage: Image{
			Key:  tool.ImageKey,
			URL:  tool.ImageURL,
			Text: tool.DisplayName,
		},
	}

	directory := ""
	if options.ShowDirectory && detection.Cwd != "" {
		directory = directoryState(detection.Cwd, options.DirectoryBasenameOnly)
		activity.State = directory
	} else if options.Collection {
		activity.State = CollectionState(detection.Others)
	}
	if options.ToolName {
		activity.Details = renderDetails(options.DetailsFormat, tool.DisplayName, directory)
	}
	if options.SmallImage && len(detection.Others) > 0 {
		other := detection.Others[0]
		activity.SmallImage = Image{
			Key:  other.ImageKey,
			URL:  other.ImageURL,
			Text: other.DisplayName,
		}
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

func renderDetails(format, toolName, directory string) string {
	if format == "" {
		format = "Using {tool}"
	}
	details := strings.ReplaceAll(format, "{tool}", toolName)
	details = strings.ReplaceAll(details, "{dir}", directory)
	return strings.TrimSpace(details)
}

// CollectionState summarizes the other running tools for Discord's single state line.
func CollectionState(others []registry.Tool) string {
	const maxTools = 3
	if len(others) == 0 {
		return ""
	}
	count := len(others)
	if count > maxTools {
		count = maxTools
	}
	state := "also: " + others[0].DisplayName
	for _, tool := range others[1:count] {
		state += " · " + tool.DisplayName
	}
	return state
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
