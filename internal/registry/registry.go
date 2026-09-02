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

	"github.com/polter-dev/discord_terminal_presence/internal/terminaltext"
)

const (
	MaxButtonCount       = 2
	MaxButtonLabelLength = 32
	MaxButtonURLLength   = 512
	MaxToolIDLength      = 64
	MinDisplayNameLength = 2
	MaxDisplayNameLength = 128
	MaxImageValueLength  = 256

	// MaxIdentityFieldBytes is the shared byte cap for process identity
	// surfaces. Sharing the bound with the detector keeps eagerly bounded
	// fields and registry-derived argv surfaces identical in both byte limit
	// and UTF-8 truncation behavior.
	MaxIdentityFieldBytes = 4096

	// MaxCustomRegexLength bounds a user-supplied match/exclude regex's raw
	// length before it is compiled. RE2 rules out catastrophic backtracking
	// (checked, not assumed, for #577), but match cost is still
	// O(len(input) x len(program)), and the compiled program's size scales
	// with pattern length: #577 measured a 240-byte value expanding to a
	// roughly 4000-state program, costing about 44ms per process, per scan,
	// against a long process identity string. This cap keeps the compiled
	// program small enough that one user regex cannot dominate a scan. The
	// largest built-in catalog regex is well under 150 characters.
	MaxCustomRegexLength = 200

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

var versionedPythonInterpreterPattern = regexp.MustCompile(`^(python\d+(\.\d+)*|pypy\d*(\.\d+)*)$`)

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

// processIdentity contains only values derived from a single process. Keep its
// identities raw: exact-name matching and regex/exclude matching intentionally
// apply their existing normalization at different points.
type processIdentity struct {
	identities       []string
	subcommand       string
	shellInterpreter bool
}

// BoundIdentityField truncates a process identity to MaxIdentityFieldBytes
// without splitting a multi-byte UTF-8 rune.
func BoundIdentityField(s string) string {
	if len(s) <= MaxIdentityFieldBytes {
		return s
	}
	b := s[:MaxIdentityFieldBytes]
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
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

// firstDisallowedRune returns the rune-offset position and codepoint of the
// first terminal escape byte, C0/C1 control character, or Unicode bidi
// formatting control in value — the same class terminaltext.Sanitize strips
// at rendering boundaries. There is no legitimate use for a control
// character in Discord-facing display text, so config load rejects it
// outright rather than silently stripping or truncating it (see #419).
// Rejecting a bidi formatting control (e.g. U+200F RIGHT-TO-LEFT MARK) is a
// deliberate anti-spoofing decision, not an accident of reusing the same
// stripper terminal rendering uses; controlCharacterError names the actual
// codepoint so a legitimate RTL display name isn't told it contains a
// "control character" when what it actually contains is a bidi mark.

// compileUserRegex compiles a user-supplied match/exclude regex value the
// same way registry matching wraps it for case-insensitive matching
// ("(?i:" + value + ")"), and is the single place that does so: config-load
// validation (ValidateCustomTool) and registry construction (newFromTools)
// both call it instead of keeping their own copy of the wrapping logic.
//
// It rejects two things beyond what compiling the wrapped value alone would
// catch (#577):
//
//   - An overlong pattern. RE2 rules out catastrophic backtracking, but
//     match cost is still O(len(input) x len(program)), and the compiled
//     program's size scales with pattern length, so an unbounded regex value
//     can still make every process-scan match expensive. MaxCustomRegexLength
//     bounds the compiled program's size indirectly by bounding the source.
//   - A value that is not a valid regex on its own but happens to close the
//     wrapper group early, such as "a)|(.*" compiling successfully only
//     because it closes the "(?i:" wrapper's own paren. Compiling the raw
//     value standalone first catches that before it is ever wrapped, so the
//     raw string stays safe to reuse in a different context later.
func compileUserRegex(field, value string) (*regexp.Regexp, error) {
	if utf8.RuneCountInString(value) > MaxCustomRegexLength {
		return nil, fmt.Errorf("%s must be at most %d characters", field, MaxCustomRegexLength)
	}
	if _, err := regexp.Compile(value); err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	re, err := regexp.Compile("(?i:" + value + ")")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return re, nil
}
func firstDisallowedRune(value string) (position int, r rune, found bool) {
	for _, candidate := range value {
		if terminaltext.IsControlOrBidi(candidate) {
			return position, candidate, true
		}
		position++
	}
	return 0, 0, false
}

// controlCharacterError names the offending codepoint and its position so a
// user does not have to guess which of "control character" or "bidi
// formatting control" they actually typed.
func controlCharacterError(field string, r rune, position int) error {
	return fmt.Errorf("%s must not contain control characters (found U+%04X at position %d)", field, r, position)
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
		if position, r, found := firstDisallowedRune(button.Label); found {
			return controlCharacterError(fmt.Sprintf("buttons[%d].label", i), r, position)
		}
		if utf8.RuneCountInString(button.URL) > MaxButtonURLLength {
			return fmt.Errorf("buttons[%d].url must be at most %d characters", i, MaxButtonURLLength)
		}
		if err := ValidateHTTPURL(button.URL); err != nil {
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
	displayNameLength := utf8.RuneCountInString(tool.DisplayName)
	if displayNameLength < MinDisplayNameLength {
		return fmt.Errorf("display_name must be at least %d characters", MinDisplayNameLength)
	}
	if displayNameLength > MaxDisplayNameLength {
		return fmt.Errorf("display_name must be at most %d characters", MaxDisplayNameLength)
	}
	if position, r, found := firstDisallowedRune(tool.DisplayName); found {
		return controlCharacterError("display_name", r, position)
	}
	if tool.Match.Regex != "" {
		if _, err := compileUserRegex("match.regex", tool.Match.Regex); err != nil {
			return err
		}
	}
	if tool.Exclude != "" {
		if _, err := compileUserRegex("exclude", tool.Exclude); err != nil {
			return err
		}
	}
	resolved := Tool{
		ImageKey:   tool.ImageKey,
		ImageURL:   tool.ImageURL,
		IconSlug:   tool.IconSlug,
		IconSource: tool.IconSource,
	}
	resolveIcon(&resolved)
	imageKeyField := "resolved image_key"
	if tool.ImageKey != "" {
		imageKeyField = "image_key"
	}
	imageURLField := "resolved image_url"
	if tool.ImageURL != "" {
		imageURLField = "image_url"
	}
	if utf8.RuneCountInString(resolved.ImageKey) > MaxImageValueLength {
		return fmt.Errorf("%s must be at most %d characters", imageKeyField, MaxImageValueLength)
	}
	// image_url is validated as an absolute HTTP(S) URL below, which since
	// #444 also rejects control, C1, and bidi runes explicitly — url.ParseRequestURI
	// alone rejects only raw ASCII controls, and that gap let U+202E through to
	// the wire. image_key still needs its own check here: it is free text with no
	// URL parse step at all (#422 review — this was the one field
	// ValidateCustomTool did not gate, and it reached the wire unsanitized).
	if position, r, found := firstDisallowedRune(resolved.ImageKey); found {
		return controlCharacterError(imageKeyField, r, position)
	}
	if utf8.RuneCountInString(resolved.ImageURL) > MaxImageValueLength {
		return fmt.Errorf("%s must be at most %d characters", imageURLField, MaxImageValueLength)
	}
	if resolved.ImageURL != "" {
		if err := ValidateHTTPURL(resolved.ImageURL); err != nil {
			return fmt.Errorf("%s must be a valid absolute http/https URL", imageURLField)
		}
	}
	if err := ValidateButtons(tool.Buttons); err != nil {
		return err
	}
	return nil
}

// ValidateHTTPURL requires an absolute HTTP or HTTPS URL. url.ParseRequestURI
// rejects ASCII control characters but nothing else, so it does not by itself
// stop a C1 control or a Unicode bidi formatting control (e.g. U+202E
// RIGHT-TO-LEFT OVERRIDE) from reaching the wire in an image or button URL,
// where it could make the visible link text read differently from the
// address it resolves to (#444). normalizeActivity deliberately does not
// sanitize URLs — sanitizing one would corrupt it — so this config-load
// check is the only defence, and it rejects rather than strips: silently
// rewriting a URL is worse than refusing it.
func ValidateHTTPURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("invalid URL")
	}
	if _, _, found := firstDisallowedRune(value); found {
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
	identity := processIdentityForMatch(process)

	for _, tool := range r.tools {
		if !tool.matchesProcess(identity) {
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
			re, err := compileUserRegex("match.regex", compiled[i].Match.Regex)
			if err != nil {
				return nil, err
			}
			compiled[i].Match.compiled = re
		}
		if compiled[i].Exclude != "" {
			re, err := compileUserRegex("exclude", compiled[i].Exclude)
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

func (t Tool) matchesProcess(identity processIdentity) bool {
	if identity.shellInterpreter {
		return false
	}

	matched := false

	if t.Match.Name != "" {
		matchName := normalizeName(t.Match.Name)
		for _, candidate := range identity.identities {
			if strings.EqualFold(normalizeName(candidate), matchName) {
				matched = true
				break
			}
		}
	}

	if !matched && t.Match.compiled != nil {
		for _, candidate := range identity.identities {
			// Catalog regexes are written with Unix separators; normalize Windows paths.
			if t.Match.compiled.MatchString(strings.ReplaceAll(candidate, `\`, "/")) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return false
	}

	if t.compiledExclude != nil {
		excludeSurfaces := identity.identities
		if identity.subcommand != "" {
			excludeSurfaces = append(append([]string(nil), identity.identities...), identity.subcommand)
		}
		for _, surface := range excludeSurfaces {
			if t.compiledExclude.MatchString(strings.ReplaceAll(surface, `\`, "/")) {
				return false
			}
		}
	}
	return true
}

func processIdentityForMatch(process ProcessInfo) processIdentity {
	if isShellInterpreterProcess(process) {
		return processIdentity{shellInterpreter: true}
	}
	identities, subcommand := processMatchIdentity(process)
	return processIdentity{
		identities: identities,
		subcommand: subcommand,
	}
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

	argv0 := BoundIdentityField(argv[0])
	entrypointIndex := 0
	if isToolInterpreter(argv0) {
		entrypointIndex = 1
		if len(argv) > 2 && isPythonInterpreter(argv0) && argv[1] == "-m" {
			entrypointIndex = 2
		}
		if entrypointIndex < len(argv) {
			entrypoint := BoundIdentityField(argv[entrypointIndex])
			identities = uniqueNonEmpty(append(identities, entrypoint)...)
		}
	}

	subcommandIndex := entrypointIndex + 1
	if subcommandIndex < len(argv) {
		return identities, BoundIdentityField(argv[subcommandIndex])
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
	name := strings.ToLower(normalizeName(candidate))
	_, ok := toolInterpreterNames[name]
	return ok || versionedPythonInterpreterPattern.MatchString(name)
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
		tool.ImageURL = fmt.Sprintf(simpleIconsURLTemplate, url.QueryEscape(slug))
	case IconSourceLobeHub:
		// url.PathEscape (not QueryEscape) matches how the slug is used here:
		// it is templated into a URL path segment, not a query value. It
		// escapes '/' as %2F, so a slug such as "../../../../@evil/pkg@1.0.0/payload"
		// can no longer leave the intended package path, and it also
		// neutralizes '?', '#', and raw spaces the same way #489 closed the
		// equivalent hole in the Simple Icons branch above (#575).
		tool.ImageURL = fmt.Sprintf(lobehubURLTemplate, url.PathEscape(slug))
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
