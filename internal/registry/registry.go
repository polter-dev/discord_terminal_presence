package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
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
	for _, customTool := range custom {
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

// Tools returns a copy of the registry entries.
func (r *Registry) Tools() []Tool {
	tools := make([]Tool, len(r.tools))
	copy(tools, r.tools)
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
	haystack := process.Exe + " " + process.Cmdline
	if strings.TrimSpace(haystack) == "" {
		haystack = process.Name
	}
	if t.compiledExclude != nil && t.compiledExclude.MatchString(haystack) {
		return false
	}

	if t.Match.Name != "" {
		matchName := normalizeName(t.Match.Name)
		for _, candidate := range []string{process.Name, process.Argv0, process.Exe} {
			if strings.EqualFold(normalizeName(candidate), matchName) {
				return true
			}
		}

		if process.Argv0 == "" {
			if argv0 := argv0FromCmdline(process.Cmdline); strings.EqualFold(normalizeName(argv0), matchName) {
				return true
			}
		}
	}

	if t.Match.compiled != nil {
		if t.Match.compiled.MatchString(haystack) {
			return true
		}
	}
	return false
}

func compareTools(left, right Tool) int {
	if left.Priority != right.Priority {
		return left.Priority - right.Priority
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
	return filepath.Base(name)
}

func argv0FromCmdline(cmdline string) string {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
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
