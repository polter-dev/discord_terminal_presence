package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

// SetupSaveFunc persists the setup config and returns the path written.
type SetupSaveFunc func(config.Config) (string, error)

// SetupInstallFunc installs autostart for exe.
type SetupInstallFunc func(exe string) error

// SetupUninstallFunc removes autostart.
type SetupUninstallFunc func() error

// SetupExeFunc resolves the executable path used for autostart.
type SetupExeFunc func() (string, error)

type setupChoice struct {
	label string
	value bool
	apply func(*config.Config, bool)
}

// SetupModel is the testable Bubble Tea onboarding wizard.
type SetupModel struct {
	choices      []setupChoice
	cursor       int
	step         int
	save         SetupSaveFunc
	install      SetupInstallFunc
	uninstall    SetupUninstallFunc
	exe          SetupExeFunc
	cfg          config.Config
	path         string
	err          error
	applied      bool
	applying     bool
	autostart    string
	width        int
	height       int
	styles       styles
	applyConfirm ConfirmDialog
	exitConfirm  *ConfirmDialog
}

type setupApplyResultMsg struct {
	cfg       config.Config
	path      string
	autostart string
	err       error
}

// NewSetupModel creates the onboarding wizard seeded from cfg.
func NewSetupModel(cfg config.Config, save SetupSaveFunc, install SetupInstallFunc, uninstall SetupUninstallFunc, exe SetupExeFunc) SetupModel {
	return SetupModel{
		cfg:       cfg,
		save:      save,
		install:   install,
		uninstall: uninstall,
		exe:       exe,
		choices: []setupChoice{
			{
				label: "Start termp automatically at login? (recommended)",
				value: cfg.StartAtLogin,
				apply: func(c *config.Config, v bool) {
					c.StartAtLogin = v
				},
			},
			{
				label: "Automatically install updates?",
				value: cfg.AutoUpdate,
				apply: func(c *config.Config, v bool) {
					c.AutoUpdate = v
				},
			},
			{
				label: "Show your working directory on Discord?",
				value: cfg.Privacy.ShowDirectory,
				apply: func(c *config.Config, v bool) {
					c.Privacy.ShowDirectory = v
				},
			},
		},
		styles:       defaultStyles(cfg.UI.AccentColor),
		applyConfirm: NewConfirmDialog("Apply these settings?", ConfirmYes, cfg.UI.AccentColor),
	}
}

// SetupConfig returns the config produced by the wizard.
func (m SetupModel) SetupConfig() config.Config {
	return m.cfg
}

// Applied reports whether Apply completed successfully.
func (m SetupModel) Applied() bool {
	return m.applied
}

// Err returns the latest setup error.
func (m SetupModel) Err() error {
	return m.err
}

func (m SetupModel) Init() tea.Cmd {
	return nil
}

func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case setupApplyResultMsg:
		m.applying = false
		if msg.err != nil {
			m.err = msg.err
			m.applied = false
			m.step = 0
			return m, nil
		}
		m.cfg = msg.cfg
		m.path = msg.path
		m.autostart = msg.autostart
		m.err = nil
		m.applied = true
		m.step = 2
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.applying {
			return m, nil
		}
		if m.exitConfirm != nil {
			if msg.String() == "backspace" {
				m.exitConfirm = nil
				return m, nil
			}
			dialog, selected := m.exitConfirm.Update(msg)
			m.exitConfirm = &dialog
			if selected {
				if dialog.Highlighted() == ConfirmYes {
					return m, tea.Quit
				}
				m.exitConfirm = nil
			}
			return m, nil
		}
		if msg.String() == "esc" || msg.String() == "q" {
			dialog := NewConfirmDialog("Are you sure you want to exit?", ConfirmYes, m.cfg.UI.AccentColor)
			m.exitConfirm = &dialog
			return m, nil
		}
		if m.step == 1 {
			dialog, selected := m.applyConfirm.Update(msg)
			m.applyConfirm = dialog
			if selected {
				if dialog.Highlighted() == ConfirmYes {
					m.step = 0
					return m.startApply()
				} else {
					m.step = 0
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "up", "k":
			if m.step == 0 {
				m.moveSetup(-1)
			}
		case "down", "j":
			if m.step == 0 {
				m.moveSetup(1)
			}
		case " ":
			switch m.step {
			case 0:
				if m.setupActionFocused() {
					m.applyConfirm = NewConfirmDialog("Apply these settings?", ConfirmYes, m.cfg.UI.AccentColor)
					m.step = 1
				} else {
					m.choices[m.cursor].value = !m.choices[m.cursor].value
				}
			}
		case "right":
			if m.step == 0 && !m.setupActionFocused() {
				m.choices[m.cursor].value = !m.choices[m.cursor].value
			}
		case "enter":
			switch m.step {
			case 0:
				m.applyConfirm = NewConfirmDialog("Apply these settings?", ConfirmYes, m.cfg.UI.AccentColor)
				m.step = 1
			case 2:
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m SetupModel) View() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("termp setup"))
	if m.short() {
		b.WriteByte('\n')
	} else {
		b.WriteString("\n\n")
	}
	if m.exitConfirm != nil {
		b.WriteString(m.exitConfirm.View())
		return m.fitView(b.String())
	}
	switch m.step {
	case 0:
		b.WriteString(m.choicesTable(true))
		if m.short() {
			b.WriteByte('\n')
		} else {
			b.WriteString("\n\n")
			b.WriteString(m.styles.muted.Render("↑/↓ move · space toggle · enter to apply · q quit"))
			b.WriteString("\n\n")
		}
		label := "Apply"
		if m.applying {
			label = "Applying…"
		}
		b.WriteString(m.actionButton(label, m.setupActionFocused()))
		if m.err != nil {
			b.WriteString("\n\n")
			b.WriteString(m.styles.error.Render("setup failed: " + m.err.Error()))
		}
	case 1:
		b.WriteString(m.applyConfirm.View())
	case 2:
		b.WriteString(m.summary())
	}
	return m.fitView(b.String())
}

func (m SetupModel) fitView(view string) string {
	if m.width > 0 {
		view = truncateBlock(view, m.width)
	}
	if m.height > 0 {
		view = lipgloss.NewStyle().MaxHeight(m.height).Render(view)
	}
	return view
}

func (m *SetupModel) moveSetup(delta int) {
	itemCount := len(m.choices) + 1
	m.cursor = (m.cursor + delta + itemCount) % itemCount
}

func (m SetupModel) setupActionFocused() bool {
	return m.cursor == len(m.choices)
}

func (m SetupModel) compact() bool {
	return m.width > 0 && m.width <= 40
}

func (m SetupModel) short() bool {
	return m.height > 0 && m.height <= 12
}

func (m SetupModel) choicesTable(interactive bool) string {
	rows := make([][]string, 0, len(m.choices))
	for i, choice := range m.choices {
		label := choice.label
		if m.compact() {
			switch i {
			case 0:
				label = "Start at login?"
			case 1:
				label = "Install updates?"
			case 2:
				label = "Show working directory?"
			}
		}
		if interactive && i == m.cursor {
			label = "› " + label
		} else {
			label = "  " + label
		}
		state := "○ Off"
		if choice.value {
			state = "● On"
		}
		rows = append(rows, []string{label, state})
	}

	return table.New().
		Headers("Question", "State").
		Rows(rows...).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(m.styles.focusedBorder).
		BorderRow(false).
		StyleFunc(func(rowIndex, columnIndex int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			switch {
			case rowIndex == table.HeaderRow:
				return style.Inherit(m.styles.title)
			case columnIndex == 1 && m.choices[rowIndex].value:
				return style.Inherit(m.styles.success)
			case columnIndex == 1:
				return style.Inherit(m.styles.muted)
			case interactive && rowIndex == m.cursor:
				return style.Inherit(m.styles.selected)
			default:
				return style
			}
		}).
		String()
}

func (m SetupModel) actionButton(label string, focused bool) string {
	buttonLabel := "  " + label
	if focused {
		buttonLabel = "› " + label
	}
	return accentButtonStyle(focused, m.cfg.UI.AccentColor).Render(buttonLabel)
}

func (m SetupModel) startApply() (tea.Model, tea.Cmd) {
	if m.applying {
		return m, nil
	}
	cfg := m.cfg
	for _, choice := range m.choices {
		if choice.apply != nil {
			choice.apply(&cfg, choice.value)
		}
	}
	m.applying = true
	m.err = nil
	m.applied = false
	original := m.cfg
	return m, func() tea.Msg {
		return applySetup(original, cfg, m.save, m.install, m.uninstall, m.exe)
	}
}

func applySetup(original, desired config.Config, save SetupSaveFunc, install SetupInstallFunc, uninstall SetupUninstallFunc, exe SetupExeFunc) setupApplyResultMsg {
	resolvedExe := ""
	changed := original.StartAtLogin != desired.StartAtLogin
	if changed && desired.StartAtLogin && exe != nil {
		var err error
		resolvedExe, err = exe()
		if err != nil {
			return setupApplyResultMsg{err: err}
		}
	}

	path := ""
	if save != nil {
		var err error
		path, err = save(desired)
		if err != nil {
			return setupApplyResultMsg{err: err}
		}
	}

	autostart := "unchanged (disabled)"
	if desired.StartAtLogin {
		autostart = "unchanged (enabled)"
	}
	var reconcileErr error
	if changed && desired.StartAtLogin {
		autostart = "installed"
		if install != nil {
			reconcileErr = install(resolvedExe)
		}
	} else if changed {
		autostart = "removed"
		if uninstall != nil {
			reconcileErr = uninstall()
		}
	}
	if reconcileErr == nil {
		return setupApplyResultMsg{cfg: desired, path: path, autostart: autostart}
	}

	if save == nil {
		return setupApplyResultMsg{err: reconcileErr}
	}
	_, rollbackErr := save(original)
	if rollbackErr != nil {
		return setupApplyResultMsg{err: errors.Join(reconcileErr, fmt.Errorf("restore previous config: %w", rollbackErr))}
	}
	return setupApplyResultMsg{err: reconcileErr}
}

func (m SetupModel) summary() string {
	var b strings.Builder
	b.WriteString(m.styles.success.Render("Setup applied."))
	b.WriteString("\n\n")
	if m.path != "" {
		b.WriteString("Config: " + m.path + "\n")
	}
	b.WriteString("Autostart: " + m.autostart + "\n")
	b.WriteString("Run now: termp start\n\n")
	b.WriteString("You can disable autostart later with `termp uninstall`; re-run setup or edit config to change these choices.\n")
	return b.String()
}
