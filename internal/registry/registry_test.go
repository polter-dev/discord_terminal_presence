package registry

import (
	"math"
	"net/url"
	"strings"
	"testing"
)

func TestRegistryMatchBuiltInByName(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.Match("/usr/local/bin/claude")
	if !ok {
		t.Fatal("expected claude to match")
	}
	if tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", tool.ID)
	}
}

func TestRegistryMatchByRegex(t *testing.T) {
	reg, err := New(Tool{
		ID:          "vim-family",
		DisplayName: "Vim",
		Match:       MatchSpec{Regex: `^vimx?$`},
		ImageKey:    "vim-family",
	})
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.Match("vimx")
	if !ok {
		t.Fatal("expected regex tool to match")
	}
	if tool.ID != "vim-family" {
		t.Fatalf("tool ID = %q, want vim-family", tool.ID)
	}
}

func TestRegistryMatchProcessClaudeVersionBinaryByArgv0(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.MatchProcess(ProcessInfo{
		Name:    "2.1.201",
		Exe:     "/home/u/.local/share/claude/versions/2.1.201",
		Cmdline: "claude --dangerously-skip-permissions",
	})
	if !ok {
		t.Fatal("expected claude version binary to match")
	}
	if tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", tool.ID)
	}
}

func TestRegistryMatchProcessClaudeVersionBinaryByExeRegex(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.MatchProcess(ProcessInfo{
		Name:    "2.1.201",
		Exe:     "/home/u/.local/share/claude/versions/2.1.201",
		Cmdline: "2.1.201 --worker",
	})
	if !ok {
		t.Fatal("expected claude version binary exe path to match")
	}
	if tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", tool.ID)
	}
}

func TestRegistryMatchProcessClaudeExcludesHelpers(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	for _, cmdline := range []string{
		"claude bg-spare --bg-spare /tmp/cc-daemon-501/spare.sock",
		"claude bg-pty-host --bg-pty-host /tmp/cc-daemon-501/pty.sock",
		"claude daemon run --json-path /tmp/cc-daemon-501/daemon.json",
	} {
		t.Run(cmdline, func(t *testing.T) {
			if tool, ok := reg.MatchProcess(ProcessInfo{Name: "2.1.211", Cmdline: cmdline}); ok {
				t.Fatalf("MatchProcess(%q) = %q, want no match", cmdline, tool.ID)
			}
		})
	}
}

func TestRegistryMatchProcessClaudeInteractiveSessionIsNotExcluded(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.MatchProcess(ProcessInfo{
		Name:    "2.1.211",
		Exe:     "/Users/test/.local/share/claude/versions/2.1.211",
		Cmdline: "claude -c --dangerously-skip-permissions",
	})
	if !ok {
		t.Fatal("expected interactive Claude session to match")
	}
	if tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", tool.ID)
	}
}

func TestRegistryMatchProcessDoesNotMatchIncidentalArguments(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		process ProcessInfo
	}{
		{
			name: "package path passed to less",
			process: ProcessInfo{
				Name:    "less",
				Exe:     "/usr/bin/less",
				Cmdline: "less /project/node_modules/@openai/codex/README.md",
			},
		},
		{
			name: "bare codex word passed to grep",
			process: ProcessInfo{
				Name:    "grep",
				Exe:     "/usr/bin/grep",
				Cmdline: "grep codex notes.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tool, ok := reg.MatchProcess(tt.process); ok {
				t.Fatalf("MatchProcess(%#v) = %q, want no match", tt.process, tool.ID)
			}
		})
	}
}

func TestRegistryExcludeDoesNotMatchIncidentalArguments(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.MatchProcess(ProcessInfo{
		Name:    "2.1.211",
		Exe:     "/Users/test/.local/share/claude/versions/2.1.211",
		Cmdline: "claude --config /tmp/bg-spare",
	})
	if !ok {
		t.Fatal("incidental excluded text suppressed a genuine Claude process")
	}
	if tool.ID != "claude-code" {
		t.Fatalf("tool ID = %q, want claude-code", tool.ID)
	}
}

func TestRegistryPriorityBreaksMatchTie(t *testing.T) {
	reg, err := New(
		Tool{
			ID:          "low",
			DisplayName: "Low",
			Match:       MatchSpec{Name: "same"},
			Priority:    1,
		},
		Tool{
			ID:          "high",
			DisplayName: "High",
			Match:       MatchSpec{Name: "same"},
			Priority:    10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.Match("same")
	if !ok {
		t.Fatal("expected same to match")
	}
	if tool.ID != "high" {
		t.Fatalf("tool ID = %q, want high", tool.ID)
	}
}

func TestNewWithCustomValidatesDiscordFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CustomTool)
		want   string
	}{
		{
			name: "id",
			mutate: func(tool *CustomTool) {
				tool.ID = strings.Repeat("i", MaxToolIDLength+1)
			},
			want: "id must be at most 64 characters",
		},
		{
			name: "display name",
			mutate: func(tool *CustomTool) {
				tool.DisplayName = strings.Repeat("n", MaxDisplayNameLength+1)
			},
			want: "display_name must be at most 128 characters",
		},
		{
			name: "image URL",
			mutate: func(tool *CustomTool) {
				tool.ImageURL = "not-a-url"
			},
			want: "image_url must be a valid absolute http/https URL",
		},
		{
			name: "image key length",
			mutate: func(tool *CustomTool) {
				tool.ImageURL = ""
				tool.ImageKey = strings.Repeat("k", MaxImageValueLength+1)
			},
			want: "image_key must be at most 256 characters",
		},
		{
			// image_url and icon_slug/URL are incidentally covered by
			// ValidateHTTPURL (url.ParseRequestURI rejects raw control bytes),
			// but image_key is free text with no such built-in guard — this
			// was the field #422 review found reaching the wire unsanitized.
			name: "image key control characters",
			mutate: func(tool *CustomTool) {
				tool.ImageURL = ""
				tool.ImageKey = "ev\x1b[31mil\x07key"
			},
			want: "image_key must not contain control characters",
		},
		{
			name: "resolved URL shape",
			mutate: func(tool *CustomTool) {
				tool.ImageURL = ""
				tool.IconSlug = "file:///tmp/icon.png"
				tool.IconSource = IconSourceURL
			},
			want: "resolved image_url must be a valid absolute http/https URL",
		},
		{
			name: "resolved key length",
			mutate: func(tool *CustomTool) {
				tool.ImageURL = ""
				tool.IconSlug = strings.Repeat("k", MaxImageValueLength+1)
				tool.IconSource = IconSourceKey
			},
			want: "resolved image_key must be at most 256 characters",
		},
		{
			name: "buttons",
			mutate: func(tool *CustomTool) {
				tool.Buttons = []Button{{Label: "", URL: "https://example.test"}}
			},
			want: "buttons[0].label must not be empty",
		},
		{
			name: "display name control characters",
			mutate: func(tool *CustomTool) {
				tool.DisplayName = "\x1b[31mEvil\x07Tool"
			},
			want: "display_name must not contain control characters",
		},
		{
			name: "button label control characters",
			mutate: func(tool *CustomTool) {
				tool.Buttons = []Button{{Label: "Go\x07od", URL: "https://example.test"}}
			},
			want: "buttons[0].label must not contain control characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := CustomTool{
				ID:          "custom",
				DisplayName: "Custom",
				Match:       CustomMatch{Name: "custom"},
				ImageURL:    "https://example.test/custom.png",
			}
			tt.mutate(&tool)
			if _, err := NewWithCustom(tool); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewWithCustom() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateCustomToolRejectsTooShortDisplayName(t *testing.T) {
	err := ValidateCustomTool(CustomTool{DisplayName: "界"})
	if err == nil || !strings.Contains(err.Error(), "display_name must be at least 2 characters") {
		t.Fatalf("ValidateCustomTool() error = %v, want minimum-length error", err)
	}
}

func TestValidateCustomToolRejectsInvalidRegexes(t *testing.T) {
	tests := []struct {
		name string
		tool CustomTool
		want string
	}{
		{
			name: "match regex",
			tool: CustomTool{
				DisplayName: "Custom",
				Match:       CustomMatch{Regex: "([a-z"},
			},
			want: "match.regex: error parsing regexp",
		},
		{
			name: "exclude regex",
			tool: CustomTool{
				DisplayName: "Custom",
				Exclude:     "(*bad",
			},
			want: "exclude: error parsing regexp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCustomTool(tt.tool)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCustomTool() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestValidateHTTPURLRejectsControlC1AndBidiRunes guards #444:
// url.ParseRequestURI only rejects ASCII control characters, so a bidi
// override or a C1 control could otherwise reach an image or button URL and
// make the visible link text read differently from the address it resolves
// to. ValidateHTTPURL must reject these outright rather than accept a URL
// that "parses" but still carries a spoofing-capable rune.
func TestValidateHTTPURLRejectsControlC1AndBidiRunes(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"bidi RLO", "https://example.test/a\u202ebcd"},
		{"C1 NEL", "https://example.test/a\u0085bcd"},
		{"ASCII control", "https://example.test/a\x01bcd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateHTTPURL(tt.url); err == nil {
				t.Fatalf("ValidateHTTPURL(%q) = nil, want rejection", tt.url)
			}
		})
	}
}

// TestValidateButtonsRejectsDisallowedRunesInURL and
// TestNewWithCustomRejectsDisallowedRunesInImageURL exercise the same #444
// hole at the two call sites that reach ValidateHTTPURL for Discord-facing
// URLs: button URLs and image URLs.
func TestValidateButtonsRejectsDisallowedRunesInURL(t *testing.T) {
	err := ValidateButtons([]Button{{Label: "Hi", URL: "https://example.test/a\u202ebcd"}})
	if err == nil || !strings.Contains(err.Error(), "buttons[0].url must be a valid absolute http/https URL") {
		t.Fatalf("ValidateButtons() error = %v, want URL rejection", err)
	}
}

func TestNewWithCustomRejectsDisallowedRunesInImageURL(t *testing.T) {
	tool := CustomTool{
		ID:          "bidi-image",
		DisplayName: "Bidi Image Tool",
		Match:       CustomMatch{Name: "bidi-image"},
		ImageURL:    "https://example.test/a\u202ebcd",
	}
	if _, err := NewWithCustom(tool); err == nil || !strings.Contains(err.Error(), "image_url must be a valid absolute http/https URL") {
		t.Fatalf("NewWithCustom() error = %v, want URL rejection", err)
	}
}

// TestValidateCustomToolAllowsLegitimateUnicodeDisplayNames guards against
// the #422 review's concern that rejecting bidi formatting controls could
// collaterally reject legitimate non-Latin or emoji display names: none of
// these contain U+061C/U+200E/U+200F/U+202A-E/U+2066-9 (the rejected set),
// only ordinary script characters, combining marks, joiners (ZWJ U+200D,
// Persian ZWNJ U+200C — distinct from the rejected bidi marks), variation
// selectors, and multi-codepoint emoji sequences.
func TestValidateCustomToolAllowsLegitimateUnicodeDisplayNames(t *testing.T) {
	names := []string{
		"🚀 Rocket",
		"👨‍👩‍👧‍👦 Family",
		"🇺🇸 USA Tool",
		"🇯🇵 Japan Tool",
		"café Tool",   // combining acute accent
		"Zürich Tool", // combining diaeresis
		"日本語ツール",
		"한글도구",
		"כלי עברי",
		"أداة عربية",
		"ابزار\u200cفارسی", // Persian ZWNJ (U+200C), not a rejected bidi mark
		"Ω Omega Tool",
		"Кириллица Tool",
		"ไทยเครื่องมือ",
		"கருவி தமிழ்",
		"🏳️‍🌈 Pride Tool",
		"👍🏽 ThumbsUp Tool",
		"naïve Tool",
		"Björk Tool",
		"🎉🎊 Party Tool",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCustomTool(CustomTool{DisplayName: name}); err != nil {
				t.Fatalf("ValidateCustomTool(%q) error = %v, want no error", name, err)
			}
		})
	}
}

func TestRegistryPriorityExtremesDoNotOverflow(t *testing.T) {
	high := Tool{
		ID:          "priority-max",
		DisplayName: "Maximum Priority",
		Match:       MatchSpec{Name: "priority-overflow-test"},
		Priority:    math.MaxInt64,
	}
	low := Tool{
		ID:          "priority-min",
		DisplayName: "Minimum Priority",
		Match:       MatchSpec{Name: "priority-overflow-test"},
		Priority:    math.MinInt64,
	}

	for _, tools := range [][]Tool{{high, low}, {low, high}} {
		reg, err := New(tools...)
		if err != nil {
			t.Fatal(err)
		}

		tool, ok := reg.Match("priority-overflow-test")
		if !ok {
			t.Fatal("expected priority-overflow-test to match")
		}
		if tool.ID != high.ID {
			t.Fatalf("tool ID = %q, want %q", tool.ID, high.ID)
		}
	}
}

func TestCustomToolOverridesBuiltInByID(t *testing.T) {
	reg, err := NewWithCustom(CustomTool{
		ID:          "codex-cli",
		DisplayName: "Custom Codex",
		Match:       CustomMatch{Name: "codex-custom"},
		ImageURL:    "https://example.test/codex.png",
		Priority:    200,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := reg.Match("codex"); ok {
		t.Fatal("did not expect original codex match after override")
	}

	tool, ok := reg.Match("codex-custom")
	if !ok {
		t.Fatal("expected custom codex to match")
	}
	if tool.DisplayName != "Custom Codex" || tool.ImageURL == "" {
		t.Fatalf("unexpected override: %#v", tool)
	}
}

func TestToolsReturnsDeepPublicCopies(t *testing.T) {
	reg, err := New(Tool{
		ID:          "copy-test",
		DisplayName: "Copy Test",
		Match:       MatchSpec{Regex: `copy-test`},
		Exclude:     `--helper`,
		ImageKey:    "copy-test",
		Buttons:     []Button{{Label: "Original", URL: "https://example.test/original"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var returned *Tool
	tools := reg.Tools()
	for i := range tools {
		if tools[i].ID == "copy-test" {
			returned = &tools[i]
			break
		}
	}
	if returned == nil {
		t.Fatal("copy-test tool not returned")
	}
	if returned.Match.compiled != nil || returned.compiledExclude != nil || returned.order != 0 {
		t.Fatalf("Tools returned private fields: %#v", *returned)
	}
	returned.Buttons[0].Label = "Mutated"
	returned.Buttons[0].URL = "https://example.test/mutated"

	matched, ok := reg.Match("copy-test")
	if !ok {
		t.Fatal("copy-test did not match")
	}
	if got := matched.Buttons[0]; got.Label != "Original" || got.URL != "https://example.test/original" {
		t.Fatalf("registry button mutated through Tools result: %#v", got)
	}
}

func TestResolverBuildsIconURLs(t *testing.T) {
	tests := []struct {
		name     string
		tool     Tool
		imageURL string
		imageKey string
	}{
		{
			name:     "simpleicons",
			tool:     Tool{ID: "vim", DisplayName: "Vim", Match: MatchSpec{Name: "vim"}, IconSlug: "vim", IconSource: IconSourceSimpleIcons},
			imageURL: "https://wsrv.nl/?url=cdn.simpleicons.org/vim&output=png&w=256&h=256",
		},
		{
			name:     "lobehub",
			tool:     Tool{ID: "claude", DisplayName: "Claude", Match: MatchSpec{Name: "claude"}, IconSlug: "claude-color", IconSource: IconSourceLobeHub},
			imageURL: "https://unpkg.com/@lobehub/icons-static-png@1.91.0/dark/claude-color.png",
		},
		{
			name:     "url source",
			tool:     Tool{ID: "url", DisplayName: "URL", Match: MatchSpec{Name: "url"}, IconSlug: "https://example.test/icon.png", IconSource: IconSourceURL},
			imageURL: "https://example.test/icon.png",
		},
		{
			name:     "key source",
			tool:     Tool{ID: "key", DisplayName: "Key", Match: MatchSpec{Name: "key"}, IconSlug: "uploaded-key", IconSource: IconSourceKey},
			imageKey: "uploaded-key",
		},
		{
			name:     "auto simpleicons",
			tool:     Tool{ID: "auto", DisplayName: "Auto", Match: MatchSpec{Name: "auto"}, IconSlug: "neovim"},
			imageURL: "https://wsrv.nl/?url=cdn.simpleicons.org/neovim&output=png&w=256&h=256",
		},
		{
			name:     "empty fallback",
			tool:     Tool{ID: "fallback", DisplayName: "Fallback", Match: MatchSpec{Name: "fallback"}},
			imageURL: GenericLogoURL,
		},
		{
			name:     "unknown source fallback",
			tool:     Tool{ID: "unknown", DisplayName: "Unknown", Match: MatchSpec{Name: "unknown"}, IconSlug: "thing", IconSource: "unknown"},
			imageURL: GenericLogoURL,
		},
		{
			name:     "explicit image url wins",
			tool:     Tool{ID: "explicit-url", DisplayName: "Explicit URL", Match: MatchSpec{Name: "explicit-url"}, ImageURL: "https://example.test/explicit.png", IconSlug: "vim", IconSource: IconSourceSimpleIcons},
			imageURL: "https://example.test/explicit.png",
		},
		{
			name:     "explicit image key wins",
			tool:     Tool{ID: "explicit-key", DisplayName: "Explicit Key", Match: MatchSpec{Name: "explicit-key"}, ImageKey: "asset-key", IconSlug: "vim", IconSource: IconSourceSimpleIcons},
			imageKey: "asset-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := newFromTools([]Tool{tt.tool})
			if err != nil {
				t.Fatal(err)
			}
			tools := reg.Tools()
			if len(tools) != 1 {
				t.Fatalf("len(tools) = %d, want 1", len(tools))
			}
			if tools[0].ImageURL != tt.imageURL {
				t.Fatalf("ImageURL = %q, want %q", tools[0].ImageURL, tt.imageURL)
			}
			if tools[0].ImageKey != tt.imageKey {
				t.Fatalf("ImageKey = %q, want %q", tools[0].ImageKey, tt.imageKey)
			}
		})
	}
}

func TestCustomToolIconSlugResolves(t *testing.T) {
	reg, err := NewWithCustom(CustomTool{
		ID:          "mine",
		DisplayName: "Mine",
		Match:       CustomMatch{Name: "mine"},
		IconSlug:    "neovim",
		IconSource:  IconSourceSimpleIcons,
	})
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.Match("mine")
	if !ok {
		t.Fatal("expected custom tool to match")
	}
	want := "https://wsrv.nl/?url=cdn.simpleicons.org/neovim&output=png&w=256&h=256"
	if tool.ImageURL != want {
		t.Fatalf("ImageURL = %q, want %q", tool.ImageURL, want)
	}
}

// TestCustomToolIconSlugEscapesQueryInjection is the #489 regression test: a
// slug containing "&url=" must not be able to smuggle a second "url" query
// parameter into the wsrv.nl proxy request, which would let it fetch and
// serve an attacker-controlled image instead of the intended Simple Icons
// asset. DisplayName must be a valid (2+ char) value here, or
// ValidateCustomTool rejects the tool before resolveIcon ever sees the slug.
func TestCustomToolIconSlugEscapesQueryInjection(t *testing.T) {
	const maliciousSlug = "git&url=http://evil.example/x.png"

	reg, err := NewWithCustom(CustomTool{
		ID:          "mine",
		DisplayName: "Mine",
		Match:       CustomMatch{Name: "mine"},
		IconSlug:    maliciousSlug,
		IconSource:  IconSourceSimpleIcons,
	})
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.Match("mine")
	if !ok {
		t.Fatal("expected custom tool to match")
	}

	want := "https://wsrv.nl/?url=cdn.simpleicons.org/git%26url%3Dhttp%3A%2F%2Fevil.example%2Fx.png&output=png&w=256&h=256"
	if tool.ImageURL != want {
		t.Fatalf("ImageURL = %q, want %q", tool.ImageURL, want)
	}

	parsed, err := url.Parse(tool.ImageURL)
	if err != nil {
		t.Fatalf("resolved ImageURL does not parse as a URL: %v", err)
	}
	values := parsed.Query()
	if got := len(values["url"]); got != 1 {
		t.Fatalf("query has %d \"url\" params, want exactly 1 (values=%v)", got, values["url"])
	}
	if got := values.Get("url"); got != "cdn.simpleicons.org/git&url=http://evil.example/x.png" {
		t.Fatalf("url param = %q, want the slug embedded verbatim (undecoded) as one value", got)
	}
}

func TestEmbeddedCatalogLoadsBroadCoverage(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tools := reg.Tools()
	if len(tools) <= 30 {
		t.Fatalf("len(tools) = %d, want > 30", len(tools))
	}

	for _, tool := range tools {
		if tool.ImageURL == "" && tool.ImageKey == "" {
			t.Fatalf("tool %q has no resolved image", tool.ID)
		}
	}
}

func TestEmbeddedCatalogFlagshipLogosAreSelfHosted(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	wantURLs := map[string]string{
		"claude-code": "https://termp.polter.sh/logos/claude-code.png",
		"gemini-cli":  "https://termp.polter.sh/logos/gemini-cli.png",
		"codex-cli":   "https://termp.polter.sh/logos/codex-cli.png",
		"aider":       "https://termp.polter.sh/logos/aider.png",
		"ollama":      "https://termp.polter.sh/logos/ollama.png",
	}

	for _, tool := range reg.Tools() {
		want, ok := wantURLs[tool.ID]
		if !ok {
			continue
		}
		if tool.ImageURL != want {
			t.Errorf("tool %q ImageURL = %q, want %q", tool.ID, tool.ImageURL, want)
		}
		delete(wantURLs, tool.ID)
	}

	for id := range wantURLs {
		t.Errorf("flagship tool %q not found in embedded catalog", id)
	}
}

func TestEmbeddedCatalogSampleMatches(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
	}{
		{name: "nvim", id: "nvim"},
		{name: "vim", id: "vim"},
		{name: "lazygit", id: "lazygit"},
		{name: "k9s", id: "k9s"},
		{name: "tmux", id: "tmux"},
		{name: "zellij", id: "zellij"},
		{name: "yazi", id: "yazi"},
		{name: "htop", id: "htop"},
		{name: "btop", id: "btop"},
		{name: "btm", id: "bottom"},
		{name: "lazydocker", id: "lazydocker"},
		{name: "ncdu", id: "ncdu"},
		{name: "neomutt", id: "neomutt"},
		// These short names are intentionally exact-name only. They are useful tools
		// but remain ambiguous outside process identity matching.
		{name: "lf", id: "lf"},
		{name: "mc", id: "mc"},
		{name: "task", id: "taskwarrior"},
		{name: "spt", id: "spotify-tui"},
		{name: "dust", id: "dust"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, ok := reg.Match(tt.name)
			if !ok {
				t.Fatalf("expected %q to match", tt.name)
			}
			if tool.ID != tt.id {
				t.Fatalf("Match(%q) ID = %q, want %q", tt.name, tool.ID, tt.id)
			}
		})
	}
}

func TestEmbeddedCatalogEveryBuiltInMatchesGenuineInvocation(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
	}{
		{name: "claude", id: "claude-code"},
		{name: "gemini", id: "gemini-cli"},
		{name: "codex", id: "codex-cli"},
		{name: "aider", id: "aider"},
		{name: "ollama", id: "ollama"},
		{name: "nvim", id: "nvim"},
		{name: "vim", id: "vim"},
		{name: "emacs", id: "emacs"},
		{name: "hx", id: "helix"},
		{name: "nano", id: "nano"},
		{name: "micro", id: "micro"},
		{name: "kak", id: "kakoune"},
		{name: "tmux", id: "tmux"},
		{name: "zellij", id: "zellij"},
		{name: "screen", id: "screen"},
		{name: "lazygit", id: "lazygit"},
		{name: "gitui", id: "gitui"},
		{name: "tig", id: "tig"},
		{name: "yazi", id: "yazi"},
		{name: "ranger", id: "ranger"},
		{name: "nnn", id: "nnn"},
		{name: "lf", id: "lf"},
		{name: "mc", id: "mc"},
		{name: "broot", id: "broot"},
		{name: "htop", id: "htop"},
		{name: "btop", id: "btop"},
		{name: "glances", id: "glances"},
		{name: "btm", id: "bottom"},
		{name: "gtop", id: "gtop"},
		{name: "bpytop", id: "bpytop"},
		{name: "k9s", id: "k9s"},
		{name: "lazydocker", id: "lazydocker"},
		{name: "ctop", id: "ctop"},
		{name: "kubectl-tui", id: "kubectl-tui"},
		{name: "ncdu", id: "ncdu"},
		{name: "gdu", id: "gdu"},
		{name: "task", id: "taskwarrior"},
		{name: "calcurse", id: "calcurse"},
		{name: "neomutt", id: "neomutt"},
		{name: "weechat", id: "weechat"},
		{name: "irssi", id: "irssi"},
		{name: "cmus", id: "cmus"},
		{name: "ncmpcpp", id: "ncmpcpp"},
		{name: "spt", id: "spotify-tui"},
		{name: "spotify_player", id: "spotify-player"},
		{name: "gping", id: "gping"},
		{name: "bandwhich", id: "bandwhich"},
		{name: "dust", id: "dust"},
	}

	if got, want := len(tests), len(reg.Tools()); got != want {
		t.Fatalf("genuine invocation cases = %d, built-ins = %d", got, want)
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			tool, ok := reg.MatchProcess(ProcessInfo{
				Name:    tt.name,
				Exe:     "/usr/local/bin/" + tt.name,
				Cmdline: tt.name,
				Argv0:   tt.name,
			})
			if !ok {
				t.Fatalf("expected %q to match", tt.name)
			}
			if tool.ID != tt.id {
				t.Fatalf("MatchProcess(%q) ID = %q, want %q", tt.name, tool.ID, tt.id)
			}
		})
	}
}

func TestEmbeddedCatalogWindowsExactNameMatches(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		process ProcessInfo
		id      string
	}{
		{
			name: "nvim exe",
			process: ProcessInfo{
				Name: "nvim.exe",
				Exe:  `C:\Program Files\Neovim\bin\nvim.exe`,
			},
			id: "nvim",
		},
		{
			name: "lazygit exe",
			process: ProcessInfo{
				Name: "lazygit.exe",
				Exe:  `C:\Users\me\scoop\apps\lazygit\current\lazygit.exe`,
			},
			id: "lazygit",
		},
		{
			name: "btop exe",
			process: ProcessInfo{
				Name: "btop.exe",
				Exe:  `C:\tools\btop\btop.exe`,
			},
			id: "btop",
		},
		{
			name: "tmux exe",
			process: ProcessInfo{
				Name: "tmux.exe",
				Exe:  `C:\msys64\usr\bin\tmux.exe`,
			},
			id: "tmux",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, ok := reg.MatchProcess(tt.process)
			if !ok {
				t.Fatalf("expected process to match %q", tt.id)
			}
			if tool.ID != tt.id {
				t.Fatalf("MatchProcess(%#v) ID = %q, want %q", tt.process, tool.ID, tt.id)
			}
		})
	}
}

func TestEmbeddedCatalogWindowsRegexMatches(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		process ProcessInfo
		id      string
	}{
		{
			name: "codex exe",
			process: ProcessInfo{
				Name:    "codex.exe",
				Exe:     `C:\Users\me\AppData\Local\codex\codex.exe`,
				Cmdline: `C:\Users\me\AppData\Local\codex\codex.exe exec`,
			},
			id: "codex-cli",
		},
		{
			name: "claude exe",
			process: ProcessInfo{
				Name:    "claude.exe",
				Exe:     `C:\Users\me\AppData\Local\claude\claude.exe`,
				Cmdline: `C:\Users\me\AppData\Local\claude\claude.exe --dangerously-skip-permissions`,
			},
			id: "claude-code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, ok := reg.MatchProcess(tt.process)
			if !ok {
				t.Fatalf("expected process to match %q", tt.id)
			}
			if tool.ID != tt.id {
				t.Fatalf("MatchProcess(%#v) ID = %q, want %q", tt.process, tool.ID, tt.id)
			}
		})
	}
}

func TestEmbeddedCatalogWindowsDoesNotMatchPrefixExecutable(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	if tool, ok := reg.MatchProcess(ProcessInfo{
		Name: "notnvim.exe",
		Exe:  `C:\tools\notnvim.exe`,
	}); ok {
		t.Fatalf("MatchProcess(notnvim.exe) = %q, want no match", tool.ID)
	}
}

func TestRegistryWindowsRegexExcludeIgnoresIncidentalArgumentPath(t *testing.T) {
	reg, err := New(Tool{
		ID:          "windows-regex-tool",
		DisplayName: "Windows Regex Tool",
		Match:       MatchSpec{Regex: `(^|\s|/)tool(\.exe)?(\s|$)`},
		Exclude:     `(^|/)helpers/`,
		ImageKey:    "windows-regex-tool",
	})
	if err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.MatchProcess(ProcessInfo{
		Name:    "tool.exe",
		Exe:     `C:\tools\tool.exe`,
		Cmdline: `C:\tools\tool.exe --config C:\Users\me\helpers\config.json`,
	})
	if !ok {
		t.Fatal("incidental Windows helper path suppressed a genuine tool process")
	}
	if tool.ID != "windows-regex-tool" {
		t.Fatalf("tool ID = %q, want windows-regex-tool", tool.ID)
	}
}

func TestEmbeddedCatalogDoesNotMatchShellInterpreterProcesses(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		process ProcessInfo
	}{
		{
			name: "bash launches codex",
			process: ProcessInfo{
				Name:    "bash.exe",
				Exe:     `C:\Program Files\Git\usr\bin\bash.exe`,
				Cmdline: `bash -c "codex exec --ask-for-approval never"`,
			},
		},
		{
			name: "powershell references codex",
			process: ProcessInfo{
				Name:    "powershell.exe",
				Exe:     `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
				Cmdline: `powershell -NoProfile -Command "Get-Process -Name codex"`,
			},
		},
		{
			name: "cmd launches claude",
			process: ProcessInfo{
				Name:    "cmd.exe",
				Exe:     `C:\Windows\System32\cmd.exe`,
				Cmdline: `cmd.exe /C claude --print`,
			},
		},
		{
			name: "sh launches gemini",
			process: ProcessInfo{
				Name:    "sh",
				Exe:     "/bin/sh",
				Cmdline: `sh -lc "gemini --model gemini-pro"`,
			},
		},
		{
			name: "zsh launches aider",
			process: ProcessInfo{
				Name:    "zsh",
				Exe:     "/bin/zsh",
				Cmdline: `zsh -lc "aider --model sonnet"`,
			},
		},
		{
			name: "pwsh launches ranger",
			process: ProcessInfo{
				Name:    "pwsh",
				Exe:     "/usr/local/bin/pwsh",
				Cmdline: `pwsh -NoProfile -Command "ranger"`,
			},
		},
		{
			name: "uppercase bash launches codex",
			process: ProcessInfo{
				Name:    "BASH.EXE",
				Exe:     `C:\Program Files\Git\usr\bin\bash.exe`,
				Cmdline: `BASH.EXE -c "codex exec"`,
			},
		},
		{
			name: "mixed case powershell references codex",
			process: ProcessInfo{
				Name:    "PowerShell.exe",
				Exe:     `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
				Cmdline: `PowerShell.exe -Command "Get-Command codex"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tool, ok := reg.MatchProcess(tt.process); ok {
				t.Fatalf("MatchProcess(%#v) = %q, want no match", tt.process, tool.ID)
			}
		})
	}
}

func TestEmbeddedCatalogWrapperMatches(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		process ProcessInfo
		id      string
	}{
		{
			name: "claude npm node wrapper",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/local/bin/node",
				Cmdline: "node /usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			},
			id: "claude-code",
		},
		{
			name: "claude published bin target",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/local/bin/node",
				Cmdline: "node /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe",
			},
			id: "claude-code",
		},
		{
			name: "gemini npm node wrapper",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/opt/homebrew/bin/node",
				Cmdline: "node /opt/homebrew/bin/gemini --model gemini-pro",
			},
			id: "gemini-cli",
		},
		{
			name: "gemini package path",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/bin/node",
				Cmdline: "node /usr/local/lib/node_modules/@google/gemini-cli/dist/index.js",
			},
			id: "gemini-cli",
		},
		{
			name: "codex npm node wrapper",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/opt/homebrew/bin/node",
				Cmdline: "node /opt/homebrew/bin/codex exec",
			},
			id: "codex-cli",
		},
		{
			name: "codex package path",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/bin/node",
				Cmdline: "node /usr/local/lib/node_modules/@openai/codex/bin/codex.js",
			},
			id: "codex-cli",
		},
		{
			name: "codex package path with spaces",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/bin/node",
				Cmdline: `node "/opt/CLI Tools/node_modules/@openai/codex/bin/codex.js"`,
			},
			id: "codex-cli",
		},
		{
			name: "codex structured argv",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/bin/node",
				Cmdline: "node /opt/CLI Tools/node_modules/@openai/codex/bin/codex.js",
				Argv0:   "node",
				Argv:    []string{"node", "/opt/CLI Tools/node_modules/@openai/codex/bin/codex.js"},
			},
			id: "codex-cli",
		},
		{
			name: "aider python module",
			process: ProcessInfo{
				Name:    "python3",
				Exe:     "/usr/bin/python3",
				Cmdline: "python3 -m aider --model sonnet",
			},
			id: "aider",
		},
		{
			name: "aider versioned python module",
			process: ProcessInfo{
				Name:    "python3.12",
				Exe:     "/usr/bin/python3.12",
				Cmdline: "python3.12 -m aider --model sonnet",
			},
			id: "aider",
		},
		{
			name: "ranger python script",
			process: ProcessInfo{
				Name:    "python",
				Exe:     "/usr/bin/python",
				Cmdline: "python /usr/local/bin/ranger",
			},
			id: "ranger",
		},
		{
			name: "glances python script",
			process: ProcessInfo{
				Name:    "python3",
				Exe:     "/usr/bin/python3",
				Cmdline: "python3 /usr/local/bin/glances",
			},
			id: "glances",
		},
		{
			name: "glances versioned python script",
			process: ProcessInfo{
				Name:    "python3.13",
				Exe:     "/usr/bin/python3.13",
				Cmdline: "python3.13 /usr/local/bin/glances",
			},
			id: "glances",
		},
		{
			name: "gtop node script",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/local/bin/node",
				Cmdline: "node /usr/local/bin/gtop",
			},
			id: "gtop",
		},
		{
			name: "bpytop python script",
			process: ProcessInfo{
				Name:    "python3",
				Exe:     "/usr/bin/python3",
				Cmdline: "python3 /usr/local/bin/bpytop",
			},
			id: "bpytop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, ok := reg.MatchProcess(tt.process)
			if !ok {
				t.Fatalf("expected process to match %q", tt.id)
			}
			if tool.ID != tt.id {
				t.Fatalf("MatchProcess(%#v) ID = %q, want %q", tt.process, tool.ID, tt.id)
			}
		})
	}
}

func TestEmbeddedCatalogShellExclusionKeepsRealToolsAndInterpreters(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		process ProcessInfo
		id      string
	}{
		{
			name: "real codex exe",
			process: ProcessInfo{
				Name:    "codex.exe",
				Exe:     `C:\Users\me\AppData\Local\codex\codex.exe`,
				Cmdline: `C:\Users\me\AppData\Local\codex\codex.exe exec`,
			},
			id: "codex-cli",
		},
		{
			name: "node codex package cli",
			process: ProcessInfo{
				Name:    "node",
				Exe:     "/usr/local/bin/node",
				Cmdline: "node /usr/local/lib/node_modules/@openai/codex/cli.js exec",
			},
			id: "codex-cli",
		},
		{
			name: "nvim exe",
			process: ProcessInfo{
				Name: "nvim.exe",
				Exe:  `C:\Program Files\Neovim\bin\nvim.exe`,
			},
			id: "nvim",
		},
		{
			name: "tig exe",
			process: ProcessInfo{
				Name: "tig.exe",
				Exe:  `C:\msys64\usr\bin\tig.exe`,
			},
			id: "tig",
		},
		{
			name: "tmux",
			process: ProcessInfo{
				Name: "tmux",
				Exe:  "/usr/bin/tmux",
			},
			id: "tmux",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, ok := reg.MatchProcess(tt.process)
			if !ok {
				t.Fatalf("expected process to match %q", tt.id)
			}
			if tool.ID != tt.id {
				t.Fatalf("MatchProcess(%#v) ID = %q, want %q", tt.process, tool.ID, tt.id)
			}
		})
	}
}

func TestEmbeddedCatalogDoesNotMatchUbiquitousProcesses(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"bash", "zsh", "fish", "sh", "dash",
		"ssh", "sshd", "node", "python", "python3", "ruby", "perl",
		"git", "code", "vi", "view", "less", "cat", "man", "top",
		"go", "cc", "ld",
	} {
		t.Run(name, func(t *testing.T) {
			if tool, ok := reg.Match(name); ok {
				t.Fatalf("Match(%q) = %q, want no match", name, tool.ID)
			}

			tool, ok := reg.MatchProcess(ProcessInfo{
				Name:    name,
				Exe:     "/usr/bin/" + name,
				Cmdline: name + " --version",
			})
			if ok {
				t.Fatalf("MatchProcess(%q) = %q, want no match", name, tool.ID)
			}
		})
	}
}

func TestEmbeddedCatalogWrapperRegexesDoNotMatchGenericInterpreters(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []ProcessInfo{
		{
			Name:    "node",
			Exe:     "/usr/local/bin/node",
			Cmdline: "node /srv/app/server.js",
		},
		{
			Name:    "node",
			Exe:     "/usr/local/bin/node",
			Cmdline: "node /srv/app/my-codex-helper.js",
		},
		{
			Name:    "python3",
			Exe:     "/usr/bin/python3",
			Cmdline: "python3 /srv/app/manage.py",
		},
		{
			Name:    "python",
			Exe:     "/usr/bin/python",
			Cmdline: "python /srv/app/ranger_plugin.py",
		},
		{
			Name:    "pythonish-tool",
			Exe:     "/usr/local/bin/pythonish-tool",
			Cmdline: "pythonish-tool -m aider",
		},
	}

	for _, process := range tests {
		if tool, ok := reg.MatchProcess(process); ok {
			t.Fatalf("MatchProcess(%#v) = %q, want no match", process, tool.ID)
		}
	}
}
