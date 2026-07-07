package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/presence"
)

func TestRenderCard(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 30, 0, 0, time.UTC)
	start := now.Add(-(12*time.Minute + 34*time.Second))

	tests := []struct {
		name    string
		state   CardState
		want    []string
		wantNot []string
	}{
		{
			name: "featured tool",
			state: CardState{
				Connected: true,
				Now:       now,
				Activity: &presence.Activity{
					Details:        "Using nvim",
					State:          "also: lazygit",
					LargeImage:     presence.Image{Text: "Neovim", Key: "nvim"},
					SmallImage:     presence.Image{Text: "lazygit", Key: "lazygit"},
					StartTimestamp: &start,
					Buttons:        []presence.Button{{Label: "Docs", URL: "https://example.test/docs"}},
				},
			},
			want: []string{
				"discord: connected",
				"Neovim",
				"nvim",
				"also: lazygit",
				"Using nvim",
				"elapsed: 12:34",
				"small image: lazygit",
				"buttons: Docs",
			},
		},
		{
			name: "idle",
			state: CardState{
				Connected: false,
				Now:       now,
			},
			want: []string{
				"discord: not running",
				"idle - no tool detected",
			},
			wantNot: []string{"elapsed:"},
		},
		{
			name: "hour timer and url image",
			state: CardState{
				Now: now,
				Activity: &presence.Activity{
					Details:        "Using Codex CLI",
					LargeImage:     presence.Image{Text: "Codex CLI", URL: "https://example.test/codex.png"},
					StartTimestamp: ptrTime(now.Add(-(time.Hour + 2*time.Minute + 3*time.Second))),
				},
			},
			want:    []string{"Codex CLI", "https://example.test/codex.png", "elapsed: 1:02:03"},
			wantNot: []string{"also:"},
		},
		{
			name: "no timer without start",
			state: CardState{
				Now: now,
				Activity: &presence.Activity{
					Details:    "Using ghostty",
					LargeImage: presence.Image{Text: "Ghostty", Key: "ghostty"},
				},
			},
			want:    []string{"Ghostty", "Using ghostty"},
			wantNot: []string{"elapsed:"},
		},
		{
			name: "recent detections",
			state: CardState{
				Now:    now,
				Recent: []RecentDetection{{Name: "nvim", At: now.Add(-3 * time.Second)}},
			},
			want: []string{"recent detections", "nvim  3s ago"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderCard(tt.state, DefaultCardStyles())
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("RenderCard() missing %q:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantNot {
				if strings.Contains(got, unwanted) {
					t.Fatalf("RenderCard() contains %q:\n%s", unwanted, got)
				}
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
