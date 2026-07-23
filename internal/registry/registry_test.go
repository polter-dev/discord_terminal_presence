package registry

import (
	"math"
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
			name: "aider python module",
			process: ProcessInfo{
				Name:    "python3",
				Exe:     "/usr/bin/python3",
				Cmdline: "python3 -m aider --model sonnet",
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
	}

	for _, process := range tests {
		if tool, ok := reg.MatchProcess(process); ok {
			t.Fatalf("MatchProcess(%#v) = %q, want no match", process, tool.ID)
		}
	}
}
