package registry

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

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
	ImageKey    string
	ImageURL    string
	Priority    int
	Buttons     []Button

	order int
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
	ImageKey    string
	ImageURL    string
	Priority    int
	Buttons     []Button
}

// Registry stores compiled tool matchers in deterministic order.
type Registry struct {
	tools []Tool
}

// New returns a registry containing built-ins plus custom tool overrides/extensions.
func New(custom ...Tool) (*Registry, error) {
	tools := builtinTools()
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
			ImageKey: customTool.ImageKey,
			ImageURL: customTool.ImageURL,
			Priority: customTool.Priority,
			Buttons:  append([]Button(nil), customTool.Buttons...),
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
		if compiled[i].Match.Regex == "" {
			continue
		}
		re, err := regexp.Compile("(?i:" + compiled[i].Match.Regex + ")")
		if err != nil {
			return nil, err
		}
		compiled[i].Match.compiled = re
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		return compiled[i].order < compiled[j].order
	})

	return &Registry{tools: compiled}, nil
}

func (t Tool) matchesProcess(process ProcessInfo) bool {
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
		haystack := process.Exe + " " + process.Cmdline
		if strings.TrimSpace(haystack) == "" {
			haystack = process.Name
		}
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

func builtinTools() []Tool {
	return []Tool{
		{
			ID:          "claude-code",
			DisplayName: "Claude Code",
			Match: MatchSpec{
				Name:  "claude",
				Regex: `(^|/)claude/versions/`,
			},
			ImageKey: "claude-code",
			Priority: 100,
		},
		{
			ID:          "gemini-cli",
			DisplayName: "Gemini CLI",
			Match:       MatchSpec{Name: "gemini"},
			ImageKey:    "gemini-cli",
			Priority:    100,
		},
		{
			ID:          "codex-cli",
			DisplayName: "Codex CLI",
			Match:       MatchSpec{Name: "codex"},
			ImageKey:    "codex-cli",
			Priority:    100,
		},
		{
			ID:          "lazygit",
			DisplayName: "lazygit",
			Match:       MatchSpec{Name: "lazygit"},
			ImageKey:    "lazygit",
			Priority:    50,
		},
		{
			ID:          "nvim",
			DisplayName: "Neovim",
			Match:       MatchSpec{Name: "nvim"},
			ImageKey:    "nvim",
			Priority:    40,
		},
		{
			ID:          "htop",
			DisplayName: "htop",
			Match:       MatchSpec{Name: "htop"},
			ImageKey:    "htop",
			Priority:    30,
		},
	}
}
