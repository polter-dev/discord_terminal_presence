package main

import (
	"path/filepath"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestBuildActivityDirectoryPrivacy(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "private", "project")
	allowedRoot := filepath.Join(base, "private")
	outsideRoot := filepath.Join(base, "allowed")
	tool := registry.Tool{ID: "test-tool", DisplayName: "Test Tool"}

	boolPointer := func(value bool) *bool {
		return &value
	}

	tests := []struct {
		name      string
		configure func(*config.Config)
		wantState string
	}{
		{
			name:      "default show_directory false",
			configure: func(*config.Config) {},
		},
		{
			name: "cwd outside directory allowlist",
			configure: func(cfg *config.Config) {
				cfg.Privacy.ShowDirectory = true
				cfg.Privacy.DirectoryAllowlist = []string{outsideRoot}
			},
		},
		{
			name: "per-tool show_directory false",
			configure: func(cfg *config.Config) {
				cfg.Privacy.ShowDirectory = true
				cfg.Tools[tool.ID] = config.ToolOverride{
					ShowDirectory: boolPointer(false),
				}
			},
		},
		{
			name: "cwd allowed",
			configure: func(cfg *config.Config) {
				cfg.Privacy.ShowDirectory = true
				cfg.Privacy.DirectoryAllowlist = []string{allowedRoot}
			},
			wantState: "📁 project",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.DetailsFormat = "{tool}"
			test.configure(&cfg)

			activity := buildActivity(cfg, detector.Detection{
				Tool: tool,
				Cwd:  cwd,
			}, "Fixed fallback")
			if activity == nil {
				t.Fatal("activity = nil, want outgoing activity")
			}
			if activity.State != test.wantState {
				t.Fatalf("outgoing activity state = %q, want %q", activity.State, test.wantState)
			}
		})
	}
}
