package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

	updated, _ := model.Update(key(" "))
	model = updated.(Model)
	if model.Config().Enabled {
		t.Fatal("enabled should toggle off")
	}

	model.cursor = findRow(t, model, rowPin, "codex-cli")
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if model.Config().Pin != "codex-cli" {
		t.Fatalf("pin = %q, want codex-cli", model.Config().Pin)
	}

	updated, _ = model.Update(key("s"))
	model = updated.(Model)
	if !model.Saved() {
		t.Fatal("model should report saved")
	}
	if saved.Enabled || saved.Pin != "codex-cli" {
		t.Fatalf("saved config = %#v", saved)
	}
}

func TestModelTextEdit(t *testing.T) {
	model := NewSettingsModel(config.Default(), nil, nil, nil, nil)
	model.cursor = findRow(t, model, rowText, "scan_interval")

	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
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

func TestModelLeaveFeedbackOpensConfiguredURL(t *testing.T) {
	cfg := config.Default()
	cfg.FeedbackURL = "https://example.test/feedback-form"
	var opened string
	model := NewSettingsModel(cfg, nil, nil, nil, func(url string) error {
		opened = url
		return nil
	})
	model.cursor = findRow(t, model, rowLink, "Leave feedback")

	updated, _ := model.Update(key("enter"))
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
	model.cursor = findRow(t, model, rowLink, "Leave feedback")

	updated, _ := model.Update(key("enter"))
	model = updated.(Model)

	want := "Feedback: " + config.DefaultFeedbackURL
	if !strings.Contains(model.View(), want) {
		t.Fatalf("view did not show fallback URL %q:\n%s", want, model.View())
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
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func findRow(t *testing.T, model Model, kind rowKind, idOrLabel string) int {
	t.Helper()
	for i, row := range model.rows {
		if row.kind == kind && (row.id == idOrLabel || row.label == idOrLabel) {
			return i
		}
	}
	t.Fatalf("row %v %q not found", kind, idOrLabel)
	return -1
}
