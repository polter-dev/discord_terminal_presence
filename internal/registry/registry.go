package registry

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxButtonCount       = 2
	MaxButtonLabelLength = 32
	MaxButtonURLLength   = 512
	MaxToolIDLength      = 64
	MaxDisplayNameLength = 128
	MaxImageValueLength  = 256

	// IconSourceSimpleIcons resolves a Simple Icons slug to a raster PNG. Discord activity
	// images must be raster (PNG/JPG); Simple Icons ships SVG, so it is rendered to PNG
	// on the fly through the free wsrv.nl image proxy (brand-colored by default).
	IconSourceSimpleIcons = "simpleicons"
	IconSourceLobeHub     = "lobehub"
	IconSourceURL         = "url"
	IconSourceKey         = "key"

	simpleIconsURLTemplate = "https://wsrv.nl/?url=cdn.simpleicons.org/%s&output=png&w=256&h=256"
	lobehubURLTemplate     = "https://unpkg.com/@lobehub/icons-static-png@1.91.0/dark/%s.png"

	// GenericLogoURL is termp's own raster mark, used as the fallback so a tool is never blank.
	GenericLogoURL = "https://termp.polter.sh/discord-app-icon.png"
)

//go:embed catalog.json
var catalogJSON []byte

var shellInterpreterNames = map[string]struct{}{
	"bash":       {},
	"sh":         {},
	"zsh":        {},
	"fish":       {},
	"dash":       {},
	"ash":        {},
	"ksh":        {},
	"csh":        {},
	"tcsh":       {},
	"cmd":        {},
	"powershell": {},
	"pwsh":       {},
}

var toolInterpreterNames = map[string]struct{}{
	"bun":     {},
	"deno":    {},
	"node":    {},
	"nodejs":  {},
	"perl":    {},
	"php":     {},
	"pypy":    {},
	"pypy3":   {},
	"python":  {},
	"python2": {},
	"python3": {},
	"ruby":    {},
}

// Button is a Discord activity button definition owned by a tool entry.
type Button struct {
	Label string
	URL   string
}

// MatchSpec describes how a tool matches process identity fields.
type MatchSpec struct {
	Name  string
	Regex string

	compiled *regexp.Regexp
}

// ProcessInfo is the registry's view of a process for matching.
type ProcessInfo struct {
	Name    string
	Exe     string
	Cmdline string
	Argv0   string
	Argv    []string
}

// Tool is a known terminal tool entry.
type Tool struct {
	ID          string
	DisplayName string
	Match       MatchSpec
	Exclude     string
	ImageKey    string
	ImageURL    string
	IconSlug    string
	IconSource  string
	Priority    int
	Buttons     []Button

	compiledExclude *regexp.Regexp
	order           int
}

// CustomMatch is the config-facing match shape for future TOML loading.
type CustomMatch struct {
	Name  string
	Regex string
}

// CustomTool is the config-facing shape for user-defined tool entries.
type CustomTool struct {
	ID          string
	DisplayName string
	Match       CustomMatch
	Exclude     string
	ImageKey    string
	ImageURL    string
	IconSlug    string
	IconSource  string
	Priority    int
	Buttons     []Button
}

// Registry stores compiled tool matchers in deterministic order.
type Registry struct {
	tools []Tool
}

// New returns a registry containing built-ins plus custom tool overrides/extensions.
func New(custom ...Tool) (*Registry, error) {
	tools, err := builtinTools()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(tools)+len(custom))
	for i := range tools {
		byID[tools[i].ID] = i
	}

	for _, tool := range custom {
		if idx, ok := byID[tool.ID]; ok {
			tool.order = tools[idx].order
			tools[idx] = tool
			continue
		}
		tool.order = len(tools)
		byID[tool.ID] = len(tools)
		tools = append(tools, tool)
	}

	return newFromTools(tools)
}

// NewWithCustom converts config-facing tools into runtime tool entries.
func NewWithCustom(custom ...CustomTool) (*Registry, error) {
	tools := make([]Tool, 0, len(custom))
	for i, customTool := range custom {
		if err := ValidateCustomTool(customTool); err != nil {
			return nil, fmt.Errorf("custom_tools[%d]: %w", i, err)
		}
		tools = append(tools, Tool{
			ID:          customTool.ID,
			DisplayName: customTool.DisplayName,
			Match: MatchSpec{
				Name:  customTool.Match.Name,
				Regex: customTool.Match.Regex,
			},
			Exclude:    customTool.Exclude,
			ImageKey:   customTool.ImageKey,
			ImageURL:   customTool.ImageURL,
			IconSlug:   customTool.IconSlug,
			IconSource: customTool.IconSource,
			Priority:   customTool.Priority,
			Buttons:    append([]Button(nil), customTool.Buttons...),
		})
	}
	return New(tools...)
}

// ValidateButtons enforces Discord's Rich Presence button constraints.
func ValidateButtons(buttons []Button) error {
	if len(buttons) > MaxButtonCount {
		return fmt.Errorf("buttons must contain at most %d entries", MaxButtonCount)
	}
	for i, button := range buttons {
		labelLength := utf8.RuneCountInString(button.Label)
		if labelLength == 0 {
			return fmt.Errorf("buttons[%d].label must not be empty", i)
		}
		if labelLength > MaxButtonLabelLength {
			return fmt.Errorf("buttons[%d].label must be at most %d characters", i, MaxButtonLabelLength)
		}
		if utf8.RuneCountInString(button.URL) > MaxButtonURLLength {
			return fmt.Errorf("buttons[%d].url must be at most %d characters", i, MaxButtonURLLength)
		}
		if err := validateHTTPURL(button.URL); err != nil {
			return fmt.Errorf("buttons[%d].url must be a valid absolute http/https URL", i)
		}
	}
	return nil
}

// ValidateCustomTool bounds config-facing fields that can reach Discord.
func ValidateCustomTool(tool CustomTool) error {
	if utf8.RuneCountInString(tool.ID) > MaxToolIDLength {
		return fmt.Errorf("id must be at most %d characters", MaxToolIDLength)
	}
	if utf8.RuneCountInString(tool.DisplayName) > MaxDisplayNameLength {
		return fmt.Errorf("display_name must be at most %d characters", MaxDisplayNameLength)
	}
	if tool.ImageURL != "" {
		if utf8.RuneCountInString(tool.ImageURL) > MaxImageValueLength {
			return fmt.Errorf("image_url must be at most %d characters", MaxImageValueLength)
		}
		if err := validateHTTPURL(tool.ImageURL); err != nil {
			return fmt.Errorf("image_url must be a valid absolute http/https URL")
		}
	}
	if err := ValidateButtons(tool.Buttons); err != nil {
		return err
	}
	return nil
}

func validateHTTPURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("invalid URL")
	}
	return nil
}

// Tools returns a copy of the registry entries.
func (r *Registry) Tools() []Tool {
	tools := make([]Tool, len(r.tools))
	for i, tool := range r.tools {
		tools[i] = tool.withoutPrivateFields()
	}
	return tools
}

// Match returns the highest-priority matching tool for an executable name.
func (r *Registry) Match(name string) (Tool, bool) {
	return r.MatchProcess(ProcessInfo{Name: name})
}

// MatchProcess returns the highest-priority matching tool for process identity fields.
func (r *Registry) MatchProcess(process ProcessInfo) (Tool, bool) {
	var (
		best Tool
		ok   bool
	)

	for _, tool := range r.tools {
		if !tool.matchesProcess(process) {
			continue
		}
		if !ok || compareTools(tool, best) > 0 {
			best = tool
			ok = true
		}
	}

	return best.withoutPrivateFields(), ok
}

func newFromTools(tools []Tool) (*Registry, error) {
	compiled := make([]Tool, len(tools))
	copy(compiled, tools)

	for i := range compiled {
		if compiled[i].order == 0 && i > 0 {
			compiled[i].order = i
		}
		compiled[i].Buttons = append([]Button(nil), compiled[i].Buttons...)
		resolveIcon(&compiled[i])
		if compiled[i].Match.Regex != "" {
			re, err := regexp.Compile("(?i:" + compiled[i].Match.Regex + ")")
			if err != nil {
				return nil, err
			}
			compiled[i].Match.compiled = re
		}
		if compiled[i].Exclude != "" {
			re, err := regexp.Compile("(?i:" + compiled[i].Exclude + ")")
			if err != nil {
				return nil, err
			}
			compiled[i].compiledExclude = re
		}
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		return compiled[i].order < compiled[j].order
	})

	return &Registry{tools: compiled}, nil
}

func (t Tool) matchesProcess(process ProcessInfo) bool {
	if isShellInterpreterProcess(process) {
		return false
	}

	identities, subcommand := processMatchIdentity(process)
	matched := false

	if t.Match.Name != "" {
		matchName := normalizeName(t.Match.Name)
		for _, candidate := range identities {
			if strings.EqualFold(normalizeName(candidate), matchName) {
				matched = true
				break
			}
		}
	}

	if !matched && t.Match.compiled != nil {
		for _, identity := range identities {
			// Catalog regexes are written with Unix separators; normalize Windows paths.
			if t.Match.compiled.MatchString(strings.ReplaceAll(identity, `\`, "/")) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return false
	}

	if t.compiledExclude != nil {
		excludeSurfaces := identities
		if subcommand != "" {
			excludeSurfaces = append(append([]string(nil), identities...), subcommand)
		}
		for _, surface := range excludeSurfaces {
			if t.compiledExclude.MatchString(strings.ReplaceAll(surface, `\`, "/")) {
				return false
			}
		}
	}
	return true
}

func processMatchIdentity(process ProcessInfo) ([]string, string) {
	argv := process.Argv
	if len(argv) == 0 {
		argv = argvFromCmdline(process.Cmdline)
	}
	identities := uniqueNonEmpty(process.Name, process.Argv0, process.Exe, argv0FromCmdline(process.Cmdline))
	if len(argv) == 0 {
		return identities, ""
	}

	entrypointIndex := 0
	if isToolInterpreter(argv[0]) {
		entrypointIndex = 1
		if len(argv) > 2 && isPythonInterpreter(argv[0]) && argv[1] == "-m" {
			entrypointIndex = 2
		}
		if entrypointIndex < len(argv) {
			identities = uniqueNonEmpty(append(identities, argv[entrypointIndex])...)
		}
	}

	subcommandIndex := entrypointIndex + 1
	if subcommandIndex < len(argv) {
		return identities, argv[subcommandIndex]
	}
	return identities, ""
}

func uniqueNonEmpty(values ...string) []string {
	unique := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func isToolInterpreter(candidate string) bool {
	_, ok := toolInterpreterNames[strings.ToLower(normalizeName(candidate))]
	return ok
}

func isPythonInterpreter(candidate string) bool {
	return strings.HasPrefix(strings.ToLower(normalizeName(candidate)), "python") ||
		strings.HasPrefix(strings.ToLower(normalizeName(candidate)), "pypy")
}

func isShellInterpreterProcess(process ProcessInfo) bool {
	for _, candidate := range []string{process.Name, process.Argv0, process.Exe} {
		name := normalizeName(candidate)
		if name == "" {
			continue
		}
		_, ok := shellInterpreterNames[strings.ToLower(name)]
		return ok
	}
	return false
}

func compareTools(left, right Tool) int {
	if left.Priority != right.Priority {
		if left.Priority < right.Priority {
			return -1
		}
		return 1
	}
	return right.order - left.order
}

func (t Tool) withoutPrivateFields() Tool {
	t.order = 0
	t.Match.compiled = nil
	t.compiledExclude = nil
	t.Buttons = append([]Button(nil), t.Buttons...)
	return t
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".com", ".bat", ".cmd":
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}

func argv0FromCmdline(cmdline string) string {
	argv := argvFromCmdline(cmdline)
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

func argvFromCmdline(cmdline string) []string {
	var (
		argv    []string
		field   strings.Builder
		quote   rune
		started bool
	)
	flush := func() {
		if !started {
			return
		}
		argv = append(argv, field.String())
		field.Reset()
		started = false
	}

	for _, current := range cmdline {
		if quote != 0 {
			if current == quote {
				quote = 0
				continue
			}
			field.WriteRune(current)
			started = true
			continue
		}

		switch current {
		case '\'', '"':
			quote = current
			started = true
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			field.WriteRune(current)
			started = true
		}
	}
	flush()
	return argv
}

func resolveIcon(tool *Tool) {
	if strings.TrimSpace(tool.ImageURL) != "" || strings.TrimSpace(tool.ImageKey) != "" {
		return
	}

	slug := strings.TrimSpace(tool.IconSlug)
	if slug == "" {
		tool.ImageURL = GenericLogoURL
		return
	}

	source := strings.ToLower(strings.TrimSpace(tool.IconSource))
	if source == "" {
		if strings.HasPrefix(slug, "http://") || strings.HasPrefix(slug, "https://") {
			source = IconSourceURL
		} else {
			source = IconSourceSimpleIcons
		}
	}

	switch source {
	case IconSourceSimpleIcons:
		tool.ImageURL = fmt.Sprintf(simpleIconsURLTemplate, slug)
	case IconSourceLobeHub:
		tool.ImageURL = fmt.Sprintf(lobehubURLTemplate, slug)
	case IconSourceURL:
		tool.ImageURL = slug
	case IconSourceKey:
		tool.ImageKey = slug
	default:
		tool.ImageURL = GenericLogoURL
	}
}

func builtinTools() ([]Tool, error) {
	var entries []catalogTool
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		return nil, fmt.Errorf("load registry catalog: %w", err)
	}

	tools := make([]Tool, 0, len(entries))
	for i, entry := range entries {
		tools = append(tools, Tool{
			ID:          entry.ID,
			DisplayName: entry.DisplayName,
			Match: MatchSpec{
				Name:  entry.Match.Name,
				Regex: entry.Match.Regex,
			},
			Exclude:    entry.Exclude,
			ImageKey:   entry.ImageKey,
			ImageURL:   entry.ImageURL,
			IconSlug:   entry.IconSlug,
			IconSource: entry.IconSource,
			Priority:   entry.Priority,
			Buttons:    append([]Button(nil), entry.Buttons...),
			order:      i,
		})
	}
	return tools, nil
}

type catalogTool struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"display_name"`
	Match       catalogMatch `json:"match"`
	Exclude     string       `json:"exclude"`
	ImageKey    string       `json:"image_key"`
	ImageURL    string       `json:"image_url"`
	IconSlug    string       `json:"icon_slug"`
	IconSource  string       `json:"icon_source"`
	Priority    int          `json:"priority"`
	Buttons     []Button     `json:"buttons"`
}

type catalogMatch struct {
	Name  string `json:"name"`
	Regex string `json:"regex"`
}
