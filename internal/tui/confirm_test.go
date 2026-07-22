package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

func TestConfirmDialogDefaultHighlightAndSelection(t *testing.T) {
	for _, tt := range []struct {
		name       string
		defaultOpt ConfirmOption
		wantMarker string
	}{
		{name: "yes", defaultOpt: ConfirmYes, wantMarker: "› YES"},
		{name: "no", defaultOpt: ConfirmNo, wantMarker: "› NO"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dialog := NewConfirmDialog("Continue?", tt.defaultOpt)
			if got := dialog.Highlighted(); got != tt.defaultOpt {
				t.Fatalf("Highlighted() = %v, want %v", got, tt.defaultOpt)
			}
			if !strings.Contains(dialog.View(), tt.wantMarker) {
				t.Fatalf("View() does not mark default option %q:\n%s", tt.wantMarker, dialog.View())
			}
		})
	}
}

func TestConfirmDialogLeftRightAndEnter(t *testing.T) {
	dialog := NewConfirmDialog("Continue?", ConfirmYes)
	dialog, selected := dialog.Update(key("right"))
	if selected || dialog.Highlighted() != ConfirmNo {
		t.Fatalf("right = (%v, %t), want (ConfirmNo, false)", dialog.Highlighted(), selected)
	}
	dialog, selected = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if selected || dialog.Highlighted() != ConfirmYes {
		t.Fatalf("h = (%v, %t), want (ConfirmYes, false)", dialog.Highlighted(), selected)
	}
	dialog, selected = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if selected || dialog.Highlighted() != ConfirmNo {
		t.Fatalf("l = (%v, %t), want (ConfirmNo, false)", dialog.Highlighted(), selected)
	}
	dialog, selected = dialog.Update(key("left"))
	if selected || dialog.Highlighted() != ConfirmYes {
		t.Fatalf("left = (%v, %t), want (ConfirmYes, false)", dialog.Highlighted(), selected)
	}
	dialog, selected = dialog.Update(key("enter"))
	if !selected || dialog.Highlighted() != ConfirmYes {
		t.Fatalf("enter = (%v, %t), want (ConfirmYes, true)", dialog.Highlighted(), selected)
	}
}

func TestSetupLeftAndBackspaceDoNotNavigateBack(t *testing.T) {
	for _, backKey := range []tea.KeyMsg{key("left"), {Type: tea.KeyBackspace}} {
		t.Run(backKey.String(), func(t *testing.T) {
			model := NewSetupModel(config.Default(), nil, nil, nil, nil)
			updated, _ := model.Update(backKey)
			model = updated.(SetupModel)
			if model.step != 0 {
				t.Fatalf("%s at first step moved to %d", backKey.String(), model.step)
			}

			updated, _ = model.Update(key("enter"))
			model = updated.(SetupModel)
			updated, _ = model.Update(backKey)
			model = updated.(SetupModel)
			if model.step != 1 {
				t.Fatalf("%s navigated away from confirmation to step %d", backKey.String(), model.step)
			}
		})
	}
}

func TestSetupContinueConfirmApplyHappyPath(t *testing.T) {
	saveCalls := 0
	model := NewSetupModel(config.Default(), func(config.Config) (string, error) {
		saveCalls++
		return "/tmp/config.toml", nil
	}, func(string) error { return nil }, nil, func() (string, error) { return "/usr/local/bin/termp", nil })

	updated, _ := model.Update(key("enter"))
	model = updated.(SetupModel)
	if model.step != 1 || saveCalls != 0 || !strings.Contains(model.View(), "Apply these settings?") {
		t.Fatalf("Apply should open confirmation without saving: step=%d saves=%d\n%s", model.step, saveCalls, model.View())
	}
	updated, cmd := model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd == nil {
		t.Fatal("confirmed Apply should return a command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)
	if model.step != 2 || !model.Applied() || saveCalls != 1 {
		t.Fatalf("confirmed Apply = step:%d applied:%t saves:%d, want 2/true/1", model.step, model.Applied(), saveCalls)
	}
}

func TestSetupApplyConfirmationNoReturnsToChoices(t *testing.T) {
	model := NewSetupModel(config.Default(), nil, nil, nil, nil)
	for _, press := range []string{"enter", "right", "enter"} {
		updated, _ := model.Update(key(press))
		model = updated.(SetupModel)
	}
	if model.step != 0 {
		t.Fatalf("choosing NO moved to step %d, want choices step 0", model.step)
	}
}

func TestSetupEscapeRequiresYesToQuit(t *testing.T) {
	model := NewSetupModel(config.Default(), nil, nil, nil, nil)
	updated, cmd := model.Update(key("esc"))
	model = updated.(SetupModel)
	if cmd != nil || model.exitConfirm == nil || model.exitConfirm.Highlighted() != ConfirmYes {
		t.Fatal("Esc should open a Yes-defaulted exit dialog without quitting")
	}

	updated, _ = model.Update(key("right"))
	model = updated.(SetupModel)
	updated, cmd = model.Update(key("enter"))
	model = updated.(SetupModel)
	if cmd != nil || model.exitConfirm != nil {
		t.Fatal("selecting NO should close the exit dialog without quitting")
	}

	updated, _ = model.Update(key("esc"))
	model = updated.(SetupModel)
	_, cmd = model.Update(key("enter"))
	assertQuitCmd(t, cmd)

	_, cmd = NewSetupModel(config.Default(), nil, nil, nil, nil).Update(key("ctrl+c"))
	assertQuitCmd(t, cmd)
}

func assertQuitCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected tea.Quit command, got nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("command returned %T, want tea.QuitMsg", cmd())
	}
}
