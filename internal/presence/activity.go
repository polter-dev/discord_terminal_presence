package presence

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/terminaltext"
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

// activityTextField describes one Discord-facing text field: where it lives on
// an Activity, what it is called in errors, and the rune bounds Discord puts on
// it. This slice is the single enumeration of Discord-facing activity text:
// normalizeActivity sanitizes and bounds every entry, and validateActivity
// checks every entry, so the two can never drift apart the way per-field call
// sites did in #402 (details/state fixed, both image tooltips missed) and #422
// (7 of 9 outbound fields fixed, both image keys missed). A wire field that is
// not represented here is caught by TestSetActivityWireStringsAreBounded, which
// walks the marshaled payload and fails on any string it has no bound for.
type activityTextField struct {
	name string
	get  func(*Activity) string
	set  func(*Activity, string)
	min  int
	max  int
}

func activityTextFields(activity *Activity) []activityTextField {
	fields := []activityTextField{
		{
			name: "name",
			get:  func(a *Activity) string { return a.Name },
			set:  func(a *Activity, v string) { a.Name = v },
			min:  0,
			max:  maxActivityTextLength,
		},
		{
			name: "details",
			get:  func(a *Activity) string { return a.Details },
			set:  func(a *Activity, v string) { a.Details = v },
			min:  minActivityTextLength,
			max:  maxActivityTextLength,
		},
		{
			name: "state",
			get:  func(a *Activity) string { return a.State },
			set:  func(a *Activity, v string) { a.State = v },
			min:  minActivityTextLength,
			max:  maxActivityTextLength,
		},
		{
			name: "large_image_text",
			get:  func(a *Activity) string { return a.LargeImage.Text },
			set:  func(a *Activity, v string) { a.LargeImage.Text = v },
			min:  minActivityTextLength,
			max:  maxActivityTextLength,
		},
		{
			name: "small_image_text",
			get:  func(a *Activity) string { return a.SmallImage.Text },
			set:  func(a *Activity, v string) { a.SmallImage.Text = v },
			min:  minActivityTextLength,
			max:  maxActivityTextLength,
		},
		{
			name: "large_image_key",
			get:  func(a *Activity) string { return a.LargeImage.Key },
			set:  func(a *Activity, v string) { a.LargeImage.Key = v },
			min:  0,
			max:  maxImageValueLength,
		},
		{
			name: "small_image_key",
			get:  func(a *Activity) string { return a.SmallImage.Key },
			set:  func(a *Activity, v string) { a.SmallImage.Key = v },
			min:  0,
			max:  maxImageValueLength,
		},
	}
	for i := range activity.Buttons {
		index := i
		fields = append(fields, activityTextField{
			name: fmt.Sprintf("buttons[%d].label", index),
			get:  func(a *Activity) string { return a.Buttons[index].Label },
			set:  func(a *Activity, v string) { a.Buttons[index].Label = v },
			// registry.ValidateButtons rejects an empty label outright, so the
			// effective minimum is 1; normalizeActivity drops a button whose
			// label sanitizes away entirely rather than failing the payload.
			min: 1,
			max: registry.MaxButtonLabelLength,
		})
	}
	return fields
}

// normalizeActivity is the single choke point every Discord-facing text field
// passes through before validation and before the wire payload is built. For
// each field in activityTextFields it sanitizes first and bounds second, in
// that order, because sanitization is not monotonically shortening: for the
// same input SanitizeSingleLine expands each line break into a 3-rune " ; "
// separator (so a value bounded to exactly 128 runes could land at 212 and be
// rejected outright — #436), and since #427 Sanitize can also return more text
// than it used to for an aborted escape sequence. Any bound applied before
// sanitization is therefore unreliable by construction. ActivityFromDetection
// no longer bounds at all (#445); it reports omission diagnostics from the
// sanitized length and leaves every bound to this choke point.
// Because the bound runs last, validateActivity's length checks are guaranteed
// to hold, so an over-long or sanitized-to-nothing field is trimmed or omitted
// instead of silently suppressing the entire presence update.
func normalizeActivity(activity Activity) Activity {
	// Activity is passed by value, but its Buttons slice still shares a backing
	// array with the caller's, and the field setters below write through
	// a.Buttons[i].Label. Copy first: writer.go holds a `desired` Activity across
	// ticks and derives `rejected` from it, so normalizing must never reach back
	// into a caller's state. (The sanitizeActivity this replaced allocated a
	// fresh button slice for the same reason.)
	if len(activity.Buttons) > 0 {
		buttons := make([]Button, len(activity.Buttons))
		copy(buttons, activity.Buttons)
		activity.Buttons = buttons
	}
	for _, field := range activityTextFields(&activity) {
		value := terminaltext.SanitizeSingleLine(field.get(&activity))
		bounded, _ := boundActivityText(value, field.min, field.max)
		field.set(&activity, bounded)
	}
	if len(activity.Buttons) > 0 {
		kept := make([]Button, 0, len(activity.Buttons))
		for _, button := range activity.Buttons {
			if button.Label == "" {
				continue
			}
			kept = append(kept, button)
		}
		if len(kept) == 0 {
			activity.Buttons = nil
		} else {
			activity.Buttons = kept
		}
	}
	return activity
}

func validateActivity(activity Activity) error {
	for _, field := range activityTextFields(&activity) {
		length := utf8.RuneCountInString(field.get(&activity))
		if length > field.max {
			return &activityValidationError{message: fmt.Sprintf("%s must be at most %d characters", field.name, field.max)}
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
		if field.image.URL != "" {
			if err := registry.ValidateHTTPURL(field.image.URL); err != nil {
				return &activityValidationError{message: fmt.Sprintf("%s URL must be a valid absolute http/https URL", field.name)}
			}
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
	// boundText no longer bounds anything: it used to apply the generic
	// min/max to the raw, pre-sanitize value, which truncates content the
	// choke point (normalizeActivity, called later by SetActivity) would
	// have kept whole, and can add a false ellipsis to a value that fits
	// once sanitized (#445). Bounding now happens exactly once, at the
	// choke point, on the sanitized text. This helper's only remaining job
	// is the omission diagnostic, and it evaluates the same post-sanitize
	// view the choke point uses so the two can never disagree: a field the
	// choke point drops (because it sanitizes below the minimum) is always
	// reported here, and a field the choke point keeps is never reported.
	boundText := func(field, value string) string {
		sanitizedLength := utf8.RuneCountInString(terminaltext.SanitizeSingleLine(value))
		if sanitizedLength > 0 && sanitizedLength < minActivityTextLength {
			omissions = append(omissions, ActivityTextOmission{
				Field:   field,
				Length:  sanitizedLength,
				Minimum: minActivityTextLength,
			})
		}
		return value
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

// boundActivityText forces value inside [min, max] runes: a non-empty value
// shorter than min is dropped (reported as omitted), and a value longer than
// max is truncated with an ellipsis. normalizeActivity is its only caller
// (post-sanitization), and it is the sole thing that guarantees the bound
// (see #436). ActivityFromDetectionWithOmissions used to call this too,
// pre-sanitization, purely to report omission diagnostics — but bounding the
// raw value truncated content the choke point would have kept once
// sanitized and could add a false ellipsis, and a field the choke point
// dropped for sanitizing below the minimum was not reported at all (#445).
// It now only checks the sanitized length against the minimum itself,
// without calling this function.
func boundActivityText(value string, min, max int) (string, bool) {
	length := utf8.RuneCountInString(value)
	if length > 0 && length < min {
		return "", true
	}
	if length <= max {
		return value, false
	}
	if max <= 0 {
		return "", false
	}
	return string([]rune(value)[:max-1]) + "…", false
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
