package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestOrderToolsByUsage(t *testing.T) {
	tools := []registry.Tool{
		{ID: "claude-code", DisplayName: "Claude Code"},
		{ID: "codex-cli", DisplayName: "Codex CLI"},
		{ID: "gemini-cli", DisplayName: "Gemini CLI"},
	}
	gotTools := OrderToolsByUsage(tools, []string{"gemini-cli", "missing"})
	got := []string{gotTools[0].ID, gotTools[1].ID, gotTools[2].ID}
	want := []string{"gemini-cli", "claude-code", "codex-cli"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OrderToolsByUsage ids = %#v, want %#v", got, want)
	}
}

func TestModelTogglePinAndSave(t *testing.T) {
	var saved config.Config
	model := NewSettingsModel(config.Default(), []registry.Tool{
		{ID: "claude-code", DisplayName: "Claude Code"},
		{ID: "codex-cli", DisplayName: "Codex CLI"},
	}, []string{"codex-cli"}, func(cfg config.Config) error {
		saved = cfg
		return nil
	}, nil)

	model = openCategory(t, model, "Global")
	updated, _ := model.Update(key(" "))
	model = updated.(Model)
	if model.Config().Enabled {
		t.Fatal("enabled should toggle off")
	}

	updated, _ = model.Update(key("left"))
	model = updated.(Model)
	model = openCategory(t, model, "Pin Specific Tool")
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if got := len(model.columns); got != 3 {
		t.Fatalf("pin drill-down columns = %d, want 3", got)
	}
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if model.Config().Pin != "codex-cli" {
		t.Fatalf("pin = %q, want codex-cli", model.Config().Pin)
	}

	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	updated, cmd := model.Update(key("s"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("save shortcut should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.Saved() {
		t.Fatal("model should report saved")
	}
	if saved.Enabled || saved.Pin != "codex-cli" {
		t.Fatalf("saved config = %#v", saved)
	}
}

func TestModelAutomaticUpdateDefaultsOffAndTogglesOn(t *testing.T) {
	model := NewSettingsModel(config.Default(), nil, nil, nil, nil)
	if model.Config().AutoUpdate {
		t.Fatal("automatic updates should default off")
	}
	model = openCategory(t, model, "Global")
	model.columns[1].cursor = findColumnRow(t, model, 1, rowToggle, "Automatic updates")
	updated, _ := model.Update(key(" "))
	model = updated.(Model)
	if !model.Config().AutoUpdate {
		t.Fatal("automatic updates should toggle on")
	}
}

func TestModelTextEdit(t *testing.T) {
	model := NewSettingsModel(config.Default(), nil, nil, nil, nil)
	model = openCategory(t, model, "Global")
	model.columns[1].cursor = findColumnRow(t, model, 1, rowText, "Scan interval")

	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	if !strings.Contains(model.View(), "type to edit  •  enter apply  •  esc cancel") {
		t.Fatalf("editing key hints not shown:\n%s", model.View())
	}
	for _, r := range "12s" {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
	}
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)

	if model.Config().ScanInterval != "3s12s" {
		t.Fatalf("scan_interval = %q, want 3s12s", model.Config().ScanInterval)
	}
}

func TestModelNavigationStaysInFocusedColumnAndBackClosesIt(t *testing.T) {
	model := NewSettingsModel(config.Default(), nil, nil, nil, nil)
	model = openCategory(t, model, "Global")

	updated, _ := model.Update(key("down"))
	model = updated.(Model)

	if got := model.columns[1].rows[model.columns[1].cursor]; got.kind != rowText || got.label != "Scan interval" {
		t.Fatalf("focused detail selection = %#v, want Scan interval", got)
	}
	if got := model.columns[0].rows[model.columns[0].cursor].label; got != "Global" {
		t.Fatalf("menu selection changed to %q while detail had focus", got)
	}

	updated, _ = model.Update(key("left"))
	model = updated.(Model)
	if got := len(model.columns); got != 1 {
		t.Fatalf("columns after left = %d, want 1", got)
	}
	if got := model.columns[0].rows[model.columns[0].cursor].label; got != "Global" {
		t.Fatalf("menu did not retain selected path: %q", got)
	}
}

func TestModelViewRendersMasterDetailColumnsAndFriendlyLabels(t *testing.T) {
	model := NewSettingsModel(config.Default(), []registry.Tool{
		{ID: "codex-cli", DisplayName: "Codex CLI"},
	}, nil, nil, nil)
	menuView := model.View()

	for _, want := range []string{
		"Categories & actions",
		"Global", "Display", "Privacy", "Headliner", "Pin Specific Tool",
		"Leave feedback", "Save & quit", "Quit without saving",
		"enter/space/right open or activate", "s save", "q/esc/ctrl+c quit",
	} {
		if !strings.Contains(menuView, want) {
			t.Errorf("menu View() missing %q:\n%s", want, menuView)
		}
	}
	if strings.Contains(menuView, "Presence enabled") {
		t.Fatalf("menu View() should not render unopened details:\n%s", menuView)
	}

	model = openCategory(t, model, "Global")
	view := model.View()
	for _, want := range []string{"Global", "State / Value", "Presence enabled", "Automatic updates", "Scan interval", "› Presence enabled", "esc/left back"} {
		if !strings.Contains(view, want) {
			t.Errorf("Global detail View() missing %q:\n%s", want, view)
		}
	}
	for _, rawKey := range []string{"scan_interval", "directory_basename_only", "headliner_idle_timeout"} {
		if strings.Contains(view, rawKey) {
			t.Errorf("View() contains raw config key %q:\n%s", rawKey, view)
		}
	}
	presenceLine := lineContaining(t, view, "Presence enabled")
	scanLine := lineContaining(t, view, "Scan interval")
	if got, want := visibleColumn(presenceLine, "Presence enabled"), visibleColumn(scanLine, "Scan interval"); got != want {
		t.Errorf("setting columns are not aligned: Presence enabled at %d, Scan interval at %d", got, want)
	}
	if got, want := visibleColumn(presenceLine, "● On"), visibleColumn(scanLine, model.Config().ScanInterval); got != want {
		t.Errorf("value columns are not aligned: ● On at %d, scan interval at %d", got, want)
	}

	for category, labels := range map[string][]string{
		"Display":   {"Tool name", "Collection label"},
		"Privacy":   {"Show folder", "Folder: name only"},
		"Headliner": {"Activity switching", "Spotlight idle timeout"},
	} {
		categoryModel := NewSettingsModel(config.Default(), nil, nil, nil, nil)
		categoryModel = openCategory(t, categoryModel, category)
		categoryView := categoryModel.View()
		for _, label := range labels {
			if !strings.Contains(categoryView, label) {
				t.Errorf("%s detail View() missing %q:\n%s", category, label, categoryView)
			}
		}
	}
}

func TestModelColumnsScrollIndependentlyAndKeepFooterVisible(t *testing.T) {
	model := NewSettingsModel(config.Default(), []registry.Tool{
		{ID: "claude-code", DisplayName: "Claude Code"},
		{ID: "codex-cli", DisplayName: "Codex CLI"},
		{ID: "gemini-cli", DisplayName: "Gemini CLI"},
		{ID: "opencode", DisplayName: "OpenCode"},
	}, nil, nil, nil)

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	model = updated.(Model)
	view := model.View()
	for _, want := range []string{"termp settings", "› Global", "s save", "q/esc/ctrl+c quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("initial View() missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Quit without saving") {
		t.Errorf("initial View() should not contain the last row in a small window:\n%s", view)
	}
	if got := lipgloss.Height(view); got > 12 {
		t.Errorf("initial View() height = %d, want at most 12:\n%s", got, view)
	}

	for model.columns[0].cursor != len(model.columns[0].rows)-1 {
		updated, _ = model.Update(key("down"))
		model = updated.(Model)
	}
	view = model.View()
	for _, want := range []string{"› Quit without saving", "s save", "q/esc/ctrl+c quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("scrolled View() missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Global") {
		t.Errorf("scrolled menu View() should not contain the first row:\n%s", view)
	}
	if got := lipgloss.Height(view); got > 12 {
		t.Errorf("scrolled View() height = %d, want at most 12:\n%s", got, view)
	}

	model = NewSettingsModel(model.Config(), []registry.Tool{
		{ID: "claude-code", DisplayName: "Claude Code"},
		{ID: "codex-cli", DisplayName: "Codex CLI"},
		{ID: "gemini-cli", DisplayName: "Gemini CLI"},
		{ID: "opencode", DisplayName: "OpenCode"},
	}, nil, nil, nil)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	model = updated.(Model)
	model = openCategory(t, model, "Pin Specific Tool")
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	for model.columns[2].cursor != len(model.columns[2].rows)-1 {
		updated, _ = model.Update(key("down"))
		model = updated.(Model)
	}
	if model.columns[2].offset == 0 {
		t.Fatal("pin choices did not scroll independently")
	}
	if model.columns[1].offset != 0 {
		t.Fatalf("parent detail offset = %d, want 0", model.columns[1].offset)
	}
	view = model.View()
	for _, want := range []string{"› OpenCode", "Search", "esc/left back", "ctrl+c quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("pin choice View() missing %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Height(view); got > 12 {
		t.Errorf("pin choice View() height = %d, want at most 12:\n%s", got, view)
	}
}

func TestModelPinDrillDownAccumulatesAndBackClosesColumns(t *testing.T) {
	model := NewSettingsModel(config.Default(), []registry.Tool{
		{ID: "codex-cli", DisplayName: "Codex CLI"},
	}, nil, nil, nil)
	model = openCategory(t, model, "Pin Specific Tool")

	view := model.View()
	for _, want := range []string{"Categories & actions", "Pin Specific Tool", "Pinned tool", "None"} {
		if !strings.Contains(view, want) {
			t.Errorf("two-column pin View() missing %q:\n%s", want, view)
		}
	}
	updated, _ := model.Update(key("right"))
	model = updated.(Model)
	if got := len(model.columns); got != 3 {
		t.Fatalf("columns after pin drill-down = %d, want 3", got)
	}
	view = model.View()
	for _, want := range []string{"Categories & actions", "› Pin Specific Tool", "› Pinned tool", "Choose a tool", "Search", "type to search tools…", "› Codex CLI"} {
		if !strings.Contains(view, want) {
			t.Errorf("three-column pin View() missing %q:\n%s", want, view)
		}
	}

	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	if got := len(model.columns); got != 2 {
		t.Fatalf("columns after esc = %d, want 2", got)
	}
	updated, _ = model.Update(key("left"))
	model = updated.(Model)
	if got := len(model.columns); got != 1 {
		t.Fatalf("columns after left = %d, want 1", got)
	}
}

func TestModelEmptyPinChoicesHaveExplicitStateAndAccurateFooter(t *testing.T) {
	model := NewSettingsModel(config.Default(), nil, nil, nil, nil)
	model = openCategory(t, model, "Pin Specific Tool")
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)

	view := model.View()
	for _, want := range []string{"no tools available", "type to search", "esc/left back", "ctrl+c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty pin choice View() missing %q:\n%s", want, view)
		}
	}
}

func TestModelPinSearchIsBoundedFuzzyAndAliasAware(t *testing.T) {
	tools := []registry.Tool{
		{ID: "claude-code", DisplayName: "Claude Code", Match: registry.MatchSpec{Name: "claude"}},
		{ID: "codex-cli", DisplayName: "Codex CLI", Match: registry.MatchSpec{Name: "codex"}},
		{ID: "gemini-cli", DisplayName: "Gemini CLI", Match: registry.MatchSpec{Name: "gemini"}},
		{ID: "helix", DisplayName: "Helix", Match: registry.MatchSpec{Name: "hx"}},
		{ID: "lazygit", DisplayName: "lazygit", Match: registry.MatchSpec{Name: "lazygit"}},
		{ID: "neovim", DisplayName: "Neovim", Match: registry.MatchSpec{Name: "nvim"}},
		{ID: "ollama", DisplayName: "Ollama", Match: registry.MatchSpec{Name: "ollama"}},
		{ID: "tmux", DisplayName: "tmux", Match: registry.MatchSpec{Name: "tmux"}},
	}

	openSearch := func() Model {
		model := NewSettingsModel(config.Default(), tools, []string{"tmux", "claude-code"}, nil, nil)
		model = openCategory(t, model, "Pin Specific Tool")
		updated, _ := model.Update(key("enter"))
		return updated.(Model)
	}

	model := openSearch()
	if got := len(model.columns[2].rows); got != maxPinResults {
		t.Fatalf("default pin results = %d, want bounded %d", got, maxPinResults)
	}
	if got := model.columns[2].rows[0].id; got != "tmux" {
		t.Fatalf("first default result = %q, want usage-ranked tmux", got)
	}
	if strings.Contains(model.View(), "Neovim") || strings.Contains(model.View(), "Ollama") {
		t.Fatalf("default search rendered more than the bounded result set:\n%s", model.View())
	}

	model = typeSearch(t, openSearch(), "cluade")
	if got := resultIDs(model); !reflect.DeepEqual(got, []string{"claude-code"}) {
		t.Fatalf("typo-tolerant results = %#v, want Claude Code", got)
	}
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	if got := model.Config().Pin; got != "claude-code" {
		t.Fatalf("pin after fuzzy result selection = %q, want claude-code", got)
	}

	model = typeSearch(t, openSearch(), "hx")
	if got := resultIDs(model); !reflect.DeepEqual(got, []string{"helix"}) {
		t.Fatalf("process-alias results = %#v, want helix", got)
	}

	model = typeSearch(t, openSearch(), "zzzzzz")
	if got := len(model.columns[2].rows); got != 0 {
		t.Fatalf("no-match results = %d, want 0", got)
	}
	if want := `no tools match "zzzzzz"`; !strings.Contains(model.View(), want) {
		t.Fatalf("no-match View() missing %q:\n%s", want, model.View())
	}
}

func TestModelViewNeverExceedsNarrowTerminalWidth(t *testing.T) {
	tools := []registry.Tool{
		{ID: "claude-code", DisplayName: "Claude Code", Match: registry.MatchSpec{Name: "claude"}},
		{ID: "codex-cli", DisplayName: "Codex CLI", Match: registry.MatchSpec{Name: "codex"}},
	}
	for _, width := range []int{40, 60} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			models := []Model{NewSettingsModel(config.Default(), tools, nil, nil, nil)}
			settings := openCategory(t, NewSettingsModel(config.Default(), tools, nil, nil, nil), "Global")
			models = append(models, settings)
			search := openCategory(t, NewSettingsModel(config.Default(), tools, nil, nil, nil), "Pin Specific Tool")
			updated, _ := search.Update(key("enter"))
			search = typeSearch(t, updated.(Model), "zzzzzz")
			models = append(models, search)

			for _, model := range models {
				updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: 24})
				view := updated.(Model).View()
				for lineNumber, line := range strings.Split(view, "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Fatalf("line %d width = %d, terminal width = %d:\n%s", lineNumber+1, got, width, view)
					}
				}
			}
		})
	}
}

func TestModelMenuSaveAndQuitActions(t *testing.T) {
	t.Run("save opens yes-defaulted confirmation and yes saves and quits", func(t *testing.T) {
		saveCalls := 0
		model := NewSettingsModel(config.Default(), nil, nil, func(config.Config) error {
			saveCalls++
			return nil
		}, nil)
		model.columns[0].cursor = findColumnRow(t, model, 0, rowSave, "Save & quit")

		updated, cmd := model.Update(key("enter"))
		model = updated.(Model)
		if cmd != nil || model.quitting || saveCalls != 0 {
			t.Fatal("selecting save should open confirmation without acting")
		}
		if model.confirm == nil || model.confirm.Highlighted() != ConfirmYes {
			t.Fatal("save confirmation should default to YES")
		}
		if !strings.Contains(model.View(), "Save changes and quit?") {
			t.Fatalf("save confirmation not rendered:\n%s", model.View())
		}

		updated, cmd = model.Update(key("enter"))
		model = updated.(Model)
		if cmd == nil || model.quitting || saveCalls != 0 || model.Saved() {
			t.Fatal("confirming save should start an asynchronous save")
		}
		updated, quitCmd := model.Update(cmd())
		model = updated.(Model)
		if quitCmd == nil || !model.quitting || saveCalls != 1 || !model.Saved() {
			t.Fatal("successful save result should mark saved and quit")
		}
	})

	t.Run("save no and back return without saving", func(t *testing.T) {
		for _, dismiss := range []string{"no", "backspace", "esc"} {
			t.Run(dismiss, func(t *testing.T) {
				saveCalls := 0
				model := NewSettingsModel(config.Default(), nil, nil, func(config.Config) error {
					saveCalls++
					return nil
				}, nil)
				model.columns[0].cursor = findColumnRow(t, model, 0, rowSave, "Save & quit")
				updated, _ := model.Update(key("enter"))
				model = updated.(Model)

				if dismiss == "no" {
					updated, _ = model.Update(key("right"))
					model = updated.(Model)
					updated, _ = model.Update(key("enter"))
				} else {
					updated, _ = model.Update(key("backspace"))
				}
				model = updated.(Model)
				if model.confirm != nil || model.quitting || saveCalls != 0 {
					t.Fatal("dismissing save confirmation should return without acting")
				}
			})
		}
	})

	t.Run("save failure remains open after confirmation", func(t *testing.T) {
		model := NewSettingsModel(config.Default(), nil, nil, func(config.Config) error {
			return errors.New("disk full")
		}, nil)
		model.columns[0].cursor = findColumnRow(t, model, 0, rowSave, "Save & quit")

		updated, _ := model.Update(key("enter"))
		model = updated.(Model)
		updated, cmd := model.Update(key("enter"))
		model = updated.(Model)
		if cmd == nil || model.quitting {
			t.Fatal("confirmed save should return a command without quitting yet")
		}
		updated, quitCmd := model.Update(cmd())
		model = updated.(Model)
		if quitCmd != nil || model.quitting {
			t.Fatal("failed save result should not quit")
		}
		if model.Err() == nil || !strings.Contains(model.View(), "save failed: disk full") {
			t.Fatalf("failed save did not remain visible:\n%s", model.View())
		}
	})

	t.Run("discard opens no-defaulted confirmation and yes quits without saving", func(t *testing.T) {
		saved := false
		model := NewSettingsModel(config.Default(), nil, nil, func(config.Config) error {
			saved = true
			return nil
		}, nil)
		model.columns[0].cursor = findColumnRow(t, model, 0, rowQuit, "Quit without saving")

		updated, cmd := model.Update(key("enter"))
		model = updated.(Model)
		if cmd != nil || model.quitting || saved {
			t.Fatal("selecting discard should open confirmation without acting")
		}
		if model.confirm == nil || model.confirm.Highlighted() != ConfirmNo {
			t.Fatal("discard confirmation should default to NO")
		}
		if !strings.Contains(model.View(), "Discard changes and quit?") {
			t.Fatalf("discard confirmation not rendered:\n%s", model.View())
		}

		updated, _ = model.Update(key("left"))
		model = updated.(Model)
		updated, cmd = model.Update(key("enter"))
		model = updated.(Model)
		if cmd == nil || !model.quitting {
			t.Fatal("confirming discard should quit")
		}
		if saved {
			t.Fatal("quit without saving called save")
		}
	})

	t.Run("discard no and back return without quitting", func(t *testing.T) {
		for _, dismiss := range []string{"no", "backspace", "esc"} {
			t.Run(dismiss, func(t *testing.T) {
				model := NewSettingsModel(config.Default(), nil, nil, nil, nil)
				model.columns[0].cursor = findColumnRow(t, model, 0, rowQuit, "Quit without saving")
				updated, _ := model.Update(key("enter"))
				model = updated.(Model)

				if dismiss == "no" {
					updated, _ = model.Update(key("enter"))
				} else {
					updated, _ = model.Update(key("backspace"))
				}
				model = updated.(Model)
				if model.confirm != nil || model.quitting {
					t.Fatal("dismissing discard confirmation should return without quitting")
				}
			})
		}
	})
}

func TestModelNarrowWidthDropsWholeLeftColumnsAndStaysGlyphSafe(t *testing.T) {
	model := NewSettingsModel(config.Default(), []registry.Tool{
		{ID: "wide", DisplayName: "界界界 Tool"},
	}, nil, nil, nil)
	model = openCategory(t, model, "Pin Specific Tool")
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)

	parentAndFocusedWidth := lipgloss.Width(model.settingsTable(1)) + 1 + lipgloss.Width(model.settingsTable(2))
	model.width = parentAndFocusedWidth
	view := model.columnsView()
	if strings.Contains(view, "Categories & actions") {
		t.Fatalf("narrow View() retained the leftmost column:\n%s", view)
	}
	for _, want := range []string{"Pin Specific Tool", "Choose a tool"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow View() missing %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Width(view); got > model.width {
		t.Fatalf("narrow columns width = %d, terminal width = %d:\n%s", got, model.width, view)
	}

	model.width = 7
	view = model.columnsView()
	if strings.Contains(view, "Pin Specific Tool") {
		t.Fatalf("very narrow View() retained the parent column:\n%s", view)
	}
	if got := lipgloss.Width(view); got > model.width {
		t.Fatalf("very narrow column width = %d, terminal width = %d:\n%s", got, model.width, view)
	}
	if !utf8.ValidString(view) || strings.ContainsRune(view, utf8.RuneError) {
		t.Fatalf("very narrow View() split a glyph: %q", view)
	}
}

func TestModelLeaveFeedbackOpensConfiguredURL(t *testing.T) {
	cfg := config.Default()
	cfg.FeedbackURL = "https://example.test/feedback-form"
	var opened string
	model := NewSettingsModel(cfg, nil, nil, nil, func(url string) error {
		opened = url
		return nil
	})
	model.columns[0].cursor = findColumnRow(t, model, 0, rowLink, "Leave feedback")

	updated, cmd := model.Update(key("enter"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("feedback action should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	if opened != cfg.FeedbackURL {
		t.Fatalf("opened URL = %q, want %q", opened, cfg.FeedbackURL)
	}
	if !strings.Contains(model.View(), "Opened feedback in your browser") {
		t.Fatalf("view did not show success status:\n%s", model.View())
	}
}

func TestModelLeaveFeedbackFailureShowsURL(t *testing.T) {
	cfg := config.Default()
	cfg.FeedbackURL = ""
	model := NewSettingsModel(cfg, nil, nil, nil, func(url string) error {
		return errors.New("no opener")
	})
	model.columns[0].cursor = findColumnRow(t, model, 0, rowLink, "Leave feedback")

	updated, cmd := model.Update(key("enter"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("feedback action should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	want := "Feedback: " + config.DefaultFeedbackURL
	if !strings.Contains(model.View(), want) {
		t.Fatalf("view did not show fallback URL %q:\n%s", want, model.View())
	}
}

func TestModelSlowCallbacksRunInCommandsAndResultsDriveState(t *testing.T) {
	t.Run("save", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		model := NewSettingsModel(config.Default(), nil, nil, func(config.Config) error {
			close(started)
			<-release
			return nil
		}, nil)

		before := time.Now()
		updated, cmd := model.Update(key("s"))
		model = updated.(Model)
		if cmd == nil || time.Since(before) > time.Second {
			t.Fatal("save Update blocked instead of returning a command")
		}
		select {
		case <-started:
			t.Fatal("save callback ran during Update")
		default:
		}
		if !model.saving || !strings.Contains(model.View(), "Saving…") {
			t.Fatalf("save in-flight state not rendered:\n%s", model.View())
		}
		if _, duplicate := model.Update(key("s")); duplicate != nil {
			t.Fatal("duplicate save should be disabled while saving")
		}

		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		<-started
		close(release)
		updated, _ = model.Update(<-result)
		model = updated.(Model)
		if model.saving || !model.Saved() || model.Err() != nil {
			t.Fatalf("save result state = saving:%t saved:%t err:%v", model.saving, model.Saved(), model.Err())
		}
	})

	t.Run("open URL", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		model := NewSettingsModel(config.Default(), nil, nil, nil, func(string) error {
			close(started)
			<-release
			return nil
		})
		model.columns[0].cursor = findColumnRow(t, model, 0, rowLink, "Leave feedback")

		updated, cmd := model.Update(key("enter"))
		model = updated.(Model)
		if cmd == nil {
			t.Fatal("open URL Update did not return a command")
		}
		select {
		case <-started:
			t.Fatal("open URL callback ran during Update")
		default:
		}
		if !model.openingURL || !strings.Contains(model.View(), "Opening…") {
			t.Fatalf("open URL in-flight state not rendered:\n%s", model.View())
		}
		if _, duplicate := model.Update(key("enter")); duplicate != nil {
			t.Fatal("duplicate open URL should be disabled while opening")
		}

		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		<-started
		close(release)
		updated, _ = model.Update(<-result)
		model = updated.(Model)
		if model.openingURL || !strings.Contains(model.View(), "Opened feedback in your browser") {
			t.Fatalf("open URL result not reflected:\n%s", model.View())
		}
	})
}

func TestSetupModelEnablingAutostartInstalls(t *testing.T) {
	var saved config.Config
	var installedExe string
	cfg := config.Default()
	cfg.StartAtLogin = false
	model := NewSetupModel(cfg, func(cfg config.Config) (string, error) {
		saved = cfg
		return "/tmp/config.toml", nil
	}, func(exe string) error {
		installedExe = exe
		return nil
	}, nil, func() (string, error) {
		return "/usr/local/bin/termp", nil
	})

	if model.step != 0 || !strings.Contains(model.View(), "Question") || strings.Contains(model.View(), "Pick the defaults") {
		t.Fatalf("setup should open directly on choices:\n%s", model.View())
	}
	if strings.Contains(model.View(), "What is this?") {
		t.Fatalf("setup choices still expose CTA setting:\n%s", model.View())
	}
	updated, _ := model.Update(key(" "))
	model = updated.(SetupModel)
	updated, _ = model.Update(key("enter"))
	model = updated.(SetupModel)
	if model.step != 1 || model.Applied() {
		t.Fatalf("enter should open confirmation without applying: step=%d applied=%t", model.step, model.Applied())
	}
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil {
		t.Fatal("confirmed setup should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if !model.Applied() {
		t.Fatal("setup should be applied")
	}
	if installedExe != "/usr/local/bin/termp" {
		t.Fatalf("installed exe = %q", installedExe)
	}
	if saved.Privacy.ShowDirectory {
		t.Fatal("default setup should keep directory display disabled")
	}
	if saved.AutoUpdate {
		t.Fatal("default setup should keep automatic updates disabled")
	}
	if !saved.CTA.Enabled {
		t.Fatal("default setup should keep CTA enabled")
	}
	if !strings.Contains(model.View(), "Setup applied") {
		t.Fatalf("summary not shown:\n%s", model.View())
	}
}

func TestSetupModelAutostartOptOutSkipsInstallAndSavesChoices(t *testing.T) {
	var saved config.Config
	installed := false
	model := NewSetupModel(config.Default(), func(cfg config.Config) (string, error) {
		saved = cfg
		return "/tmp/config.toml", nil
	}, func(exe string) error {
		installed = true
		return nil
	}, nil, func() (string, error) {
		return "/usr/local/bin/termp", nil
	})

	updated, _ := model.Update(key(" "))
	model = updated.(SetupModel)
	updated, _ = model.Update(key("down"))
	model = updated.(SetupModel)
	updated, _ = model.Update(key("down"))
	model = updated.(SetupModel)
	updated, _ = model.Update(key(" "))
	model = updated.(SetupModel)
	updated, _ = model.Update(key("down"))
	model = updated.(SetupModel)
	if !model.setupActionFocused() {
		t.Fatal("Apply button should be focused after moving past all choices")
	}
	if !strings.Contains(model.View(), "› Apply") {
		t.Fatalf("focused Apply button is not visibly selected:\n%s", model.View())
	}
	updated, _ = model.Update(key(" "))
	model = updated.(SetupModel)
	if model.step != 1 {
		t.Fatalf("space on Apply moved to step %d, want 1", model.step)
	}
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil {
		t.Fatal("confirmed setup should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if installed {
		t.Fatal("install should be skipped when autostart is disabled")
	}
	if !saved.Privacy.ShowDirectory {
		t.Fatal("show_directory choice should be saved")
	}
	if !saved.CTA.Enabled {
		t.Fatal("setup should preserve the default CTA setting")
	}
}

func TestSetupModelDisablingExistingAutostartUninstallsAndReportsResult(t *testing.T) {
	cfg := config.Default()
	cfg.StartAtLogin = true
	installCalls := 0
	uninstallCalls := 0
	var saved config.Config
	model := NewSetupModel(cfg, func(next config.Config) (string, error) {
		saved = next
		return "/tmp/config.toml", nil
	}, func(string) error {
		installCalls++
		return nil
	}, func() error {
		uninstallCalls++
		return nil
	}, func() (string, error) {
		t.Fatal("executable resolution should not run when disabling autostart")
		return "", nil
	})

	updated, _ := model.Update(key(" "))
	model = updated.(SetupModel)
	updated, _ = model.Update(key("enter"))
	model = updated.(SetupModel)
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil || installCalls != 0 || uninstallCalls != 0 {
		t.Fatal("confirmed setup should return a command without reconciling inline")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if installCalls != 0 || uninstallCalls != 1 {
		t.Fatalf("reconcile calls = install:%d uninstall:%d, want 0/1", installCalls, uninstallCalls)
	}
	if saved.StartAtLogin || model.SetupConfig().StartAtLogin || !model.Applied() {
		t.Fatalf("applied config = saved:%t model:%t applied:%t", saved.StartAtLogin, model.SetupConfig().StartAtLogin, model.Applied())
	}
	if !strings.Contains(model.View(), "Autostart: removed") {
		t.Fatalf("summary did not report removal:\n%s", model.View())
	}
}

func TestSetupModelFailedInstallRollsBackPersistedConfig(t *testing.T) {
	original := config.Default()
	original.StartAtLogin = false
	persisted := original
	saveCalls := 0
	model := NewSetupModel(original, func(next config.Config) (string, error) {
		saveCalls++
		persisted = next
		return "/tmp/config.toml", nil
	}, func(string) error {
		return errors.New("install failed")
	}, nil, func() (string, error) {
		return "/usr/local/bin/termp", nil
	})

	updated, _ := model.Update(key(" "))
	model = updated.(SetupModel)
	updated, _ = model.Update(key("enter"))
	model = updated.(SetupModel)
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil {
		t.Fatal("confirmed setup should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if saveCalls != 2 {
		t.Fatalf("save calls = %d, want desired save plus rollback", saveCalls)
	}
	if persisted.StartAtLogin || model.SetupConfig().StartAtLogin {
		t.Fatalf("failed install left inconsistent config = persisted:%t model:%t", persisted.StartAtLogin, model.SetupConfig().StartAtLogin)
	}
	if model.Applied() || model.Err() == nil || !strings.Contains(model.View(), "install failed") {
		t.Fatalf("failed install not surfaced correctly: applied:%t err:%v\n%s", model.Applied(), model.Err(), model.View())
	}
}

func TestSetupModelSlowApplyRunsInCommand(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewSetupModel(config.Default(), func(config.Config) (string, error) {
		close(started)
		<-release
		return "/tmp/config.toml", nil
	}, nil, nil, nil)

	updated, _ := model.Update(key("enter"))
	model = updated.(SetupModel)
	before := time.Now()
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil || time.Since(before) > time.Second {
		t.Fatal("setup Update blocked instead of returning a command")
	}
	select {
	case <-started:
		t.Fatal("setup callback ran during Update")
	default:
	}
	if !model.applying || !strings.Contains(model.View(), "Applying…") {
		t.Fatalf("setup in-flight state not rendered:\n%s", model.View())
	}
	if _, duplicate := model.Update(key("enter")); duplicate != nil {
		t.Fatal("duplicate apply should be disabled while applying")
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-started
	close(release)
	updated, _ = model.Update(<-result)
	model = updated.(SetupModel)
	if model.applying || !model.Applied() || model.Err() != nil {
		t.Fatalf("apply result state = applying:%t applied:%t err:%v", model.applying, model.Applied(), model.Err())
	}
}

func TestSetupModelApplyPersistsEveryToggleCombination(t *testing.T) {
	for _, tt := range []struct {
		name          string
		startAtLogin  bool
		autoUpdate    bool
		showDirectory bool
	}{
		{name: "all_on", startAtLogin: true, autoUpdate: true, showDirectory: true},
		{name: "all_off", startAtLogin: false, autoUpdate: false, showDirectory: false},
		{name: "mixed", startAtLogin: true, autoUpdate: false, showDirectory: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var saved config.Config
			var updated tea.Model
			saveCalls := 0
			model := NewSetupModel(config.Default(), func(cfg config.Config) (string, error) {
				saveCalls++
				saved = cfg
				return "/tmp/config.toml", nil
			}, func(string) error { return nil }, nil, func() (string, error) { return "/usr/local/bin/termp", nil })

			if model.choices[0].value != tt.startAtLogin {
				updated, _ = model.Update(key(" "))
				model = updated.(SetupModel)
			}
			updated, _ = model.Update(key("down"))
			model = updated.(SetupModel)
			if model.choices[1].value != tt.autoUpdate {
				updated, _ = model.Update(key(" "))
				model = updated.(SetupModel)
			}
			updated, _ = model.Update(key("down"))
			model = updated.(SetupModel)
			if model.choices[2].value != tt.showDirectory {
				updated, _ = model.Update(key(" "))
				model = updated.(SetupModel)
			}
			updated, _ = model.Update(key("enter"))
			model = updated.(SetupModel)
			updated, cmd := model.Update(key("enter"))
			model = updated.(SetupModel)
			if cmd == nil {
				t.Fatal("confirmed setup should return a command")
			}
			updated, _ = model.Update(cmd())
			model = updated.(SetupModel)

			if saveCalls != 1 {
				t.Fatalf("save calls = %d, want 1", saveCalls)
			}
			if saved.StartAtLogin != tt.startAtLogin || saved.AutoUpdate != tt.autoUpdate || saved.Privacy.ShowDirectory != tt.showDirectory {
				t.Fatalf("saved toggles = start_at_login:%t auto_update:%t show_directory:%t, want start_at_login:%t auto_update:%t show_directory:%t",
					saved.StartAtLogin, saved.AutoUpdate, saved.Privacy.ShowDirectory, tt.startAtLogin, tt.autoUpdate, tt.showDirectory)
			}
		})
	}
}

func TestSetupModelSeedsChoicesFromExistingConfig(t *testing.T) {
	cfg := config.Default()
	cfg.StartAtLogin = false
	cfg.AutoUpdate = true
	cfg.Privacy.ShowDirectory = true
	model := NewSetupModel(cfg, nil, nil, nil, nil)

	want := []bool{false, true, true}
	for i, choice := range model.choices {
		if choice.value != want[i] {
			t.Errorf("choice %q = %t, want %t", choice.label, choice.value, want[i])
		}
	}
	for _, label := range []string{"Start termp", "Automatically install updates", "working directory"} {
		line := lineContaining(t, model.View(), label)
		wantState := "● On"
		if strings.Contains(label, "Start termp") {
			wantState = "○ Off"
		}
		if !strings.Contains(line, wantState) {
			t.Errorf("%q line missing current state %q: %s", label, wantState, line)
		}
	}
}

func TestSetupModelApplyPreservesUnexposedConfig(t *testing.T) {
	enabled := false
	cfg := config.Default()
	cfg.Enabled = false
	cfg.ScanInterval = "9s"
	cfg.Display = config.Display{ToolName: false, ElapsedTimer: false, Collection: false}
	cfg.Privacy.DirectoryAllowlist = []string{"/work/private"}
	cfg.CTA = config.CTA{Enabled: false, Label: "Custom", URL: "https://example.test"}
	cfg.Tools = map[string]config.ToolOverride{"codex-cli": {Enabled: &enabled}}
	cfg.CustomTools = []registry.CustomTool{{
		ID: "custom", DisplayName: "Custom", Match: registry.CustomMatch{Name: "custom"}, ImageKey: "custom",
	}}
	var saved config.Config
	model := NewSetupModel(cfg, func(got config.Config) (string, error) {
		saved = got
		return "/tmp/config.toml", nil
	}, nil, nil, nil)

	updated, _ := model.Update(key("enter"))
	model = updated.(SetupModel)
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil {
		t.Fatal("confirmed setup should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)
	if !model.Applied() {
		t.Fatal("setup was not applied")
	}
	if saved.Enabled != cfg.Enabled || saved.ScanInterval != cfg.ScanInterval ||
		!reflect.DeepEqual(saved.Display, cfg.Display) ||
		!reflect.DeepEqual(saved.Privacy.DirectoryAllowlist, cfg.Privacy.DirectoryAllowlist) ||
		!reflect.DeepEqual(saved.CTA, cfg.CTA) ||
		!reflect.DeepEqual(saved.Tools, cfg.Tools) ||
		!reflect.DeepEqual(saved.CustomTools, cfg.CustomTools) {
		t.Fatalf("unexposed config changed:\n got: %#v\nwant: %#v", saved, cfg)
	}
}

func TestSetupModelQuitDoesNotSave(t *testing.T) {
	for _, quitKey := range []string{"q", "esc"} {
		t.Run(quitKey, func(t *testing.T) {
			saveCalls := 0
			model := NewSetupModel(config.Default(), func(config.Config) (string, error) {
				saveCalls++
				return "/tmp/config.toml", nil
			}, nil, nil, nil)

			updated, _ := model.Update(key(" "))
			model = updated.(SetupModel)
			updated, cmd := model.Update(key(quitKey))
			model = updated.(SetupModel)
			if cmd != nil {
				t.Fatal("guarded quit key should not quit before confirmation")
			}
			if model.exitConfirm == nil {
				t.Fatal("guarded quit key should open the exit dialog")
			}

			if saveCalls != 0 {
				t.Fatalf("save calls = %d, want 0", saveCalls)
			}
			if model.Applied() {
				t.Fatal("quit should not apply setup")
			}
		})
	}
}

func TestSetupModelViewsRenderTableButtonsAndFitTerminal(t *testing.T) {
	type viewCase struct {
		name string
		view string
		want []string
	}
	for _, width := range []int{80, 40} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			model := NewSetupModel(config.Default(), nil, nil, nil, nil)
			updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: 12})
			model = updated.(SetupModel)

			updateLabel := "Automatically install updates?"
			if width == 40 {
				updateLabel = "Install updates?"
			}
			steps := []viewCase{{
				name: "choices", view: model.View(), want: []string{"termp setup", "Question", "State", updateLabel, "Apply"},
			}}

			choicesView := model.View()
			startLine := lineContaining(t, choicesView, "Start")
			directoryLine := lineContaining(t, choicesView, "directory")
			if got, want := visibleColumn(startLine, "● On"), visibleColumn(directoryLine, "○ Off"); got != want {
				t.Errorf("setup state columns are not aligned: ● On at %d, ○ Off at %d\n%s", got, want, choicesView)
			}

			updated, _ = model.Update(key("enter"))
			model = updated.(SetupModel)
			steps = append(steps, viewCase{
				name: "confirm",
				view: model.View(),
				want: []string{"Apply these settings?", "YES", "NO"},
			})

			for _, step := range steps {
				for _, want := range step.want {
					if !strings.Contains(step.view, want) {
						t.Errorf("%s View() missing %q:\n%s", step.name, want, step.view)
					}
				}
				if strings.Contains(step.view, "What is this?") {
					t.Errorf("%s View() exposes removed CTA choice:\n%s", step.name, step.view)
				}
				for lineNumber, line := range strings.Split(step.view, "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Errorf("%s View() line %d width = %d, want <= %d:\n%s", step.name, lineNumber+1, got, width, step.view)
					}
				}
				if got := lipgloss.Height(step.view); got > 12 {
					t.Errorf("%s View() height = %d, want <= 12:\n%s", step.name, got, step.view)
				}
			}
		})
	}
}

func TestSetupModelShowsExplicitKeyHints(t *testing.T) {
	model := NewSetupModel(config.Default(), nil, nil, nil, nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	view := updated.(SetupModel).View()
	if want := "↑/↓ move · space toggle · enter to apply · q quit"; !strings.Contains(view, want) {
		t.Fatalf("setup hints missing %q:\n%s", want, view)
	}
}

func key(value string) tea.KeyMsg {
	switch value {
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "s":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func findColumnRow(t *testing.T, model Model, columnIndex int, kind rowKind, idOrLabel string) int {
	t.Helper()
	for i, row := range model.columns[columnIndex].rows {
		if row.kind == kind && (row.id == idOrLabel || row.label == idOrLabel) {
			return i
		}
	}
	t.Fatalf("row %v %q not found", kind, idOrLabel)
	return -1
}

func openCategory(t *testing.T, model Model, label string) Model {
	t.Helper()
	model.columns[0].cursor = findColumnRow(t, model, 0, rowCategory, label)
	updated, _ := model.Update(key("enter"))
	return updated.(Model)
}

func typeSearch(t *testing.T, model Model, query string) Model {
	t.Helper()
	for _, r := range query {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
	}
	return model
}

func resultIDs(model Model) []string {
	rows := model.columns[len(model.columns)-1].rows
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.id
	}
	return ids
}

func lineContaining(t *testing.T, text, needle string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("line containing %q not found", needle)
	return ""
}

func visibleColumn(line, needle string) int {
	index := strings.Index(line, needle)
	if index < 0 {
		return -1
	}
	return lipgloss.Width(line[:index])
}
