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
	minActivityTextLength = 2
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

// ActivityTextOmission describes a rendered optional field that was too short
// for Discord and was therefore omitted from the mapped activity.
type ActivityTextOmission struct {
	Field   string
	Length  int
	Minimum int
}

// Message formats the diagnostic for a caller-provided debug logger.
func (o ActivityTextOmission) Message() string {
	characters := "characters"
	if o.Length == 1 {
		characters = "character"
	}
	return fmt.Sprintf("presence: omitting %s: rendered value contains %d %s; minimum is %d", o.Field, o.Length, characters, o.Minimum)
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
		min   int
	}{
		{name: "name", value: activity.Name, min: 0},
		{name: "details", value: activity.Details, min: minActivityTextLength},
		{name: "state", value: activity.State, min: minActivityTextLength},
		{name: "large_image_text", value: activity.LargeImage.Text, min: minActivityTextLength},
		{name: "small_image_text", value: activity.SmallImage.Text, min: minActivityTextLength},
	}
	for _, field := range textFields {
		length := utf8.RuneCountInString(field.value)
		if length > maxActivityTextLength {
			return &activityValidationError{message: fmt.Sprintf("%s must be at most %d characters", field.name, maxActivityTextLength)}
		}
		if length > 0 && length < field.min {
			return &activityValidationError{message: fmt.Sprintf("%s must be at least %d characters when present", field.name, field.min)}
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
	activity, ok, _ := ActivityFromDetectionWithOmissions(detection, options)
	return activity, ok
}

// ActivityFromDetectionWithOmissions maps a detector result and reports optional
// text fields omitted because they did not meet Discord's minimum length.
func ActivityFromDetectionWithOmissions(detection detector.Detection, options DisplayOptions) (Activity, bool, []ActivityTextOmission) {
	if detection.None {
		return Activity{}, false, nil
	}
	var omissions []ActivityTextOmission
	boundText := func(field, value string) string {
		bounded, omitted := boundActivityText(value)
		if omitted {
			omissions = append(omissions, ActivityTextOmission{
				Field:   field,
				Length:  utf8.RuneCountInString(value),
				Minimum: minActivityTextLength,
			})
		}
		return bounded
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
			Text: boundText("large_image_text", tool.DisplayName),
		},
	}

	directory := ""
	if options.ShowDirectory && detection.Cwd != "" {
		directory = directoryState(detection.Cwd, options.DirectoryBasenameOnly)
	}
	if customizedDetailsFormat(options.DetailsFormat) {
		if directory != "" {
			activity.State = directory
		} else if options.ToolName && options.Collection {
			activity.State = legacyCollectionState(detection.Others)
		}
		if options.ToolName {
			activity.Details = renderDetails(options.DetailsFormat, tool.DisplayName, directory)
		}
	} else {
		collection := ""
		if options.ToolName && options.Collection {
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
	activity.Details = boundText("details", activity.Details)
	activity.State = boundText("state", activity.State)
	if options.SmallImage && len(detection.Others) > 0 {
		other := detection.Others[0]
		activity.SmallImage = Image{
			Key:  other.ImageKey,
			URL:  other.ImageURL,
			Text: boundText("small_image_text", other.DisplayName),
		}
	}
	if options.ElapsedTimer && !detection.StartedAt.IsZero() {
		startedAt := detection.StartedAt
		activity.StartTimestamp = &startedAt
	}
	if options.Buttons {
		activity.Buttons = buttonsFromTool(tool)
	}

	return activity, true, omissions
}

func boundActivityText(value string) (string, bool) {
	length := utf8.RuneCountInString(value)
	if length > 0 && length < minActivityTextLength {
		return "", true
	}
	if length <= maxActivityTextLength {
		return value, false
	}
	return string([]rune(value)[:maxActivityTextLength-1]) + "…", false
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
	directory := DirectoryDisplay(cwd, basenameOnly)
	if directory == "" {
		return ""
	}
	return "📁 " + directory
}

// DirectoryDisplay reduces a directory path to the components permitted for display.
func DirectoryDisplay(cwd string, basenameOnly bool) string {
	clean := filepath.Clean(cwd)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == filepath.VolumeName(clean)+string(filepath.Separator) {
		return ""
	}
	if basenameOnly {
		return base
	}
	parent := filepath.Base(filepath.Dir(clean))
	if parent == "." || parent == string(filepath.Separator) || parent == filepath.VolumeName(clean)+string(filepath.Separator) {
		return base
	}
	return parent + "/" + base
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
