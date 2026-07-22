package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmOption identifies one of the two choices in a ConfirmDialog.
type ConfirmOption int

const (
	ConfirmYes ConfirmOption = iota
	ConfirmNo
)

// ConfirmDialog is a reusable, presentational Yes/No prompt.
type ConfirmDialog struct {
	prompt      string
	highlighted ConfirmOption
	accent      lipgloss.Style
	muted       lipgloss.Style
}

// NewConfirmDialog creates a dialog with the requested initial highlight.
func NewConfirmDialog(prompt string, defaultOption ConfirmOption) ConfirmDialog {
	if defaultOption != ConfirmNo {
		defaultOption = ConfirmYes
	}
	return ConfirmDialog{
		prompt:      prompt,
		highlighted: defaultOption,
		accent:      lipgloss.NewStyle().Bold(true).Foreground(tuiPalette.accent),
		muted:       lipgloss.NewStyle().Foreground(tuiPalette.muted),
	}
}

// Highlighted returns the option currently selected by the cursor.
func (d ConfirmDialog) Highlighted() ConfirmOption {
	return d.highlighted
}

// Update handles horizontal navigation and reports whether Enter selected an option.
func (d ConfirmDialog) Update(msg tea.KeyMsg) (ConfirmDialog, bool) {
	switch msg.String() {
	case "left", "h":
		d.highlighted = ConfirmYes
	case "right", "l":
		d.highlighted = ConfirmNo
	case "enter":
		return d, true
	}
	return d, false
}

// View renders the prompt and horizontally arranged Yes/No choices.
func (d ConfirmDialog) View() string {
	return d.accent.Render(d.prompt) + "\n\n" + lipgloss.JoinHorizontal(
		lipgloss.Top,
		d.button("YES", ConfirmYes),
		"  ",
		d.button("NO", ConfirmNo),
	) + "\n\n" + d.muted.Render("←/h →/l choose  •  enter select")
}

func (d ConfirmDialog) button(label string, option ConfirmOption) string {
	style := accentButtonStyle(false).Inherit(d.accent)
	if d.highlighted == option {
		style = accentButtonStyle(true)
		label = "› " + label
	} else {
		label = "  " + label
	}
	return style.Render(strings.TrimRight(label, " "))
}
