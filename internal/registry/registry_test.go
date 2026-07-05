package registry

import "testing"

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

func TestEmbeddedCatalogSampleMatches(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]string{
		"nvim":    "nvim",
		"lazygit": "lazygit",
		"k9s":     "k9s",
		"tmux":    "tmux",
	}

	for name, wantID := range tests {
		tool, ok := reg.Match(name)
		if !ok {
			t.Fatalf("expected %q to match", name)
		}
		if tool.ID != wantID {
			t.Fatalf("Match(%q) ID = %q, want %q", name, tool.ID, wantID)
		}
	}
}

func TestEmbeddedCatalogDoesNotMatchUbiquitousProcesses(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"bash", "zsh", "fish", "sh", "node", "python", "ruby", "perl", "ssh", "git", "vi", "view", "less", "cat"} {
		if tool, ok := reg.Match(name); ok {
			t.Fatalf("Match(%q) = %q, want no match", name, tool.ID)
		}
	}
}
