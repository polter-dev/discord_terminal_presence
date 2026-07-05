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
