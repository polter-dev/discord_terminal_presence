package presence

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// DefaultAppID is termp's public Discord Application ID. An application ID is
// public (it is sent to every Discord client to render presence) and safe to embed and
// commit — it is not a secret and requires no bot token.
const DefaultAppID = "1523168764793847918"

const defaultDetailsFormat = "Using {tool}"

const (
	maxActivityTextLength = 128
	maxImageValueLength   = 256
)

// DisplayOptions is the M2 stand-in for future config-driven display/privacy settings.
type DisplayOptions struct {
	ToolName              bool
	DetailsFormat         string
	FallbackMessage       string
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
		DetailsFormat:         defaultDetailsFormat,
		FallbackMessage:       "Working on something",
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
	Name           string
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

type activityValidationError struct {
	message string
}

func (e *activityValidationError) Error() string {
	return "presence: invalid activity: " + e.message
}

func validateActivity(activity Activity) error {
	textFields := []struct {
		name  string
		value string
	}{
		{name: "name", value: activity.Name},
		{name: "details", value: activity.Details},
		{name: "state", value: activity.State},
		{name: "large_image_text", value: activity.LargeImage.Text},
		{name: "small_image_text", value: activity.SmallImage.Text},
	}
	for _, field := range textFields {
		if utf8.RuneCountInString(field.value) > maxActivityTextLength {
			return &activityValidationError{message: fmt.Sprintf("%s must be at most %d characters", field.name, maxActivityTextLength)}
		}
	}
	imageFields := []struct {
		name  string
		image Image
	}{
		{name: "large_image", image: activity.LargeImage},
		{name: "small_image", image: activity.SmallImage},
	}
	for _, field := range imageFields {
		value := imageValue(field.image)
		if utf8.RuneCountInString(value) > maxImageValueLength {
			return &activityValidationError{message: fmt.Sprintf("%s must be at most %d characters", field.name, maxImageValueLength)}
		}
		if field.image.URL != "" && !validHTTPURL(field.image.URL) {
			return &activityValidationError{message: fmt.Sprintf("%s URL must be a valid absolute http/https URL", field.name)}
		}
	}
	buttons := make([]registry.Button, 0, len(activity.Buttons))
	for _, button := range activity.Buttons {
		buttons = append(buttons, registry.Button{Label: button.Label, URL: button.URL})
	}
	if err := registry.ValidateButtons(buttons); err != nil {
		return &activityValidationError{message: err.Error()}
	}
	return nil
}

func validHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
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
		Name: tool.DisplayName,
		LargeImage: Image{
			Key:  tool.ImageKey,
			URL:  tool.ImageURL,
			Text: tool.DisplayName,
		},
	}

	directory := ""
	if options.ShowDirectory && detection.Cwd != "" {
		directory = directoryState(detection.Cwd, options.DirectoryBasenameOnly)
	}
	if customizedDetailsFormat(options.DetailsFormat) {
		if directory != "" {
			activity.State = directory
		} else if options.Collection {
			activity.State = legacyCollectionState(detection.Others)
		}
		if options.ToolName {
			activity.Details = renderDetails(options.DetailsFormat, tool.DisplayName, directory)
		}
	} else if options.ToolName {
		collection := ""
		if options.Collection {
			collection = CollectionState(detection.Others)
		}
		switch {
		case collection != "":
			activity.Details = collection
			activity.State = directory
		case directory != "":
			activity.Details = directory
		default:
			activity.Details = options.FallbackMessage
		}
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
		format = defaultDetailsFormat
	}
	details := strings.ReplaceAll(format, "{tool}", toolName)
	details = strings.ReplaceAll(details, "{dir}", directory)
	return strings.TrimSpace(details)
}

func customizedDetailsFormat(format string) bool {
	return format != "" && format != defaultDetailsFormat
}

// CollectionState summarizes the other running tools for Discord's details line.
func CollectionState(others []registry.Tool) string {
	return collectionState("With ", others)
}

func legacyCollectionState(others []registry.Tool) string {
	return collectionState("With ", others)
}

func collectionState(prefix string, others []registry.Tool) string {
	const maxTools = 3
	if len(others) == 0 {
		return ""
	}
	count := len(others)
	if count > maxTools {
		count = maxTools
	}
	state := prefix + others[0].DisplayName
	for _, tool := range others[1:count] {
		state += " · " + tool.DisplayName
	}
	return state
}

func directoryState(cwd string, basenameOnly bool) string {
	clean := filepath.Clean(cwd)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == filepath.VolumeName(clean)+string(filepath.Separator) {
		return ""
	}
	if basenameOnly {
		return "📁 " + base
	}
	parent := filepath.Base(filepath.Dir(clean))
	if parent == "." || parent == string(filepath.Separator) || parent == filepath.VolumeName(clean)+string(filepath.Separator) {
		return "📁 " + base
	}
	return "📁 " + parent + "/" + base
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
