package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
)

func TestAccentColorAppliesAcrossTUIScreens(t *testing.T) {
	cfg := config.Default()
	cfg.UI.AccentColor = "#12ABEF"
	want := lipgloss.AdaptiveColor{Light: "#12ABEF", Dark: "#12ABEF"}

	watch := NewWatchModelWithConfig(cfg, time.Now)
	if got := watch.styles.Title.GetForeground(); got != want {
		t.Fatalf("watch accent = %#v, want %#v", got, want)
	}

	settings := NewSettingsModel(cfg, nil, nil, nil, nil)
	if got := settings.styles.focusedBorder.GetForeground(); got != want {
		t.Fatalf("settings accent = %#v, want %#v", got, want)
	}

	setup := NewSetupModel(cfg, nil, nil, nil, nil)
	if got := setup.styles.title.GetForeground(); got != want {
		t.Fatalf("setup accent = %#v, want %#v", got, want)
	}
	if got := setup.applyConfirm.accent.GetForeground(); got != want {
		t.Fatalf("setup confirmation accent = %#v, want %#v", got, want)
	}
}

func TestWatchModelActivityAndConnection(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	model := NewWatchModelWithClock(func() time.Time { return now })
	activity := &presence.Activity{
		Details:    "Using nvim",
		LargeImage: presence.Image{Text: "Neovim", Key: "nvim"},
	}

	updated, _ := model.Update(ActivityMsg{Activity: activity, FeaturedName: "Neovim"})
	model = updated.(WatchModel)
	if model.CurrentActivity() != activity {
		t.Fatal("activity was not stored")
	}
	if got := model.Recent(); len(got) != 1 || got[0].Name != "Neovim" || !got[0].At.Equal(now) {
		t.Fatalf("recent = %#v, want Neovim at %v", got, now)
	}

	updated, _ = model.Update(ConnMsg(true))
	model = updated.(WatchModel)
	if !model.Connected() {
		t.Fatal("connected = false, want true")
	}
	if view := model.View(); !strings.Contains(view, "termp watch - live Discord preview") || !strings.Contains(view, "Neovim") {
		t.Fatalf("view missing header or activity:\n%s", view)
	}
}

func TestWatchModelRecentOnlyChangesOnFeaturedChange(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	model := NewWatchModelWithClock(func() time.Time { return now })

	for _, name := range []string{"nvim", "nvim", "lazygit"} {
		updated, _ := model.Update(ActivityMsg{
			Activity:     &presence.Activity{LargeImage: presence.Image{Text: name, Key: name}},
			FeaturedName: name,
		})
		model = updated.(WatchModel)
		now = now.Add(time.Second)
		model.now = now
	}

	got := model.Recent()
	if len(got) != 2 {
		t.Fatalf("recent len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "lazygit" || got[1].Name != "nvim" {
		t.Fatalf("recent order = %#v, want lazygit then nvim", got)
	}
}

func TestWatchModelRecentCapsAtFive(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	model := NewWatchModelWithClock(func() time.Time { return now })

	for _, name := range []string{"one", "two", "three", "four", "five", "six"} {
		model.now = now
		updated, _ := model.Update(ActivityMsg{
			Activity:     &presence.Activity{LargeImage: presence.Image{Text: name, Key: name}},
			FeaturedName: name,
		})
		model = updated.(WatchModel)
		now = now.Add(time.Second)
	}

	got := model.Recent()
	if len(got) != 5 {
		t.Fatalf("recent len = %d, want 5: %#v", len(got), got)
	}
	if got[0].Name != "six" || got[4].Name != "two" {
		t.Fatalf("recent = %#v, want newest six and oldest retained two", got)
	}
}

func TestWatchModelClearedActivityAllowsSameFeatureAgain(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	model := NewWatchModelWithClock(func() time.Time { return now })

	updated, _ := model.Update(ActivityMsg{Activity: &presence.Activity{LargeImage: presence.Image{Text: "nvim"}}, FeaturedName: "nvim"})
	model = updated.(WatchModel)
	updated, _ = model.Update(ActivityMsg{})
	model = updated.(WatchModel)
	updated, _ = model.Update(ActivityMsg{Activity: &presence.Activity{LargeImage: presence.Image{Text: "nvim"}}, FeaturedName: "nvim"})
	model = updated.(WatchModel)

	if got := model.Recent(); len(got) != 2 {
		t.Fatalf("recent len = %d, want 2 after clear and re-detect: %#v", len(got), got)
	}
}

func TestWatchModelTickAndQuit(t *testing.T) {
	model := NewWatchModelWithClock(func() time.Time {
		return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	})
	next := time.Date(2026, 7, 7, 12, 0, 1, 0, time.UTC)

	updated, cmd := model.Update(tickMsg(next))
	model = updated.(WatchModel)
	if !model.now.Equal(next) {
		t.Fatalf("now = %v, want %v", model.now, next)
	}
	if cmd == nil {
		t.Fatal("tick should reschedule")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	model = updated.(WatchModel)
	if !model.Quitting() {
		t.Fatal("quitting = false, want true")
	}
	if cmd == nil {
		t.Fatal("quit should return tea.Quit")
	}
}
