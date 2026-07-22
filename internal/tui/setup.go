package tui

import (
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
	exe          SetupExeFunc
	cfg          config.Config
	path         string
	err          error
	applied      bool
	width        int
	height       int
	styles       styles
	applyConfirm ConfirmDialog
	exitConfirm  *ConfirmDialog
}

// NewSetupModel creates the onboarding wizard with privacy-first defaults.
func NewSetupModel(save SetupSaveFunc, install SetupInstallFunc, exe SetupExeFunc) SetupModel {
	cfg := config.Default()
	return SetupModel{
		cfg:     cfg,
		save:    save,
		install: install,
		exe:     exe,
		choices: []setupChoice{
			{
				label: "Start termp automatically at login? (recommended)",
				value: cfg.StartAtLogin,
				apply: func(c *config.Config, v bool) {
					c.StartAtLogin = v
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
		styles:       defaultStyles(),
		applyConfirm: NewConfirmDialog("Apply these settings?", ConfirmYes),
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
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
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
			dialog := NewConfirmDialog("Are you sure you want to exit?", ConfirmYes)
			m.exitConfirm = &dialog
			return m, nil
		}
		if m.step == 2 {
			if msg.String() == "left" || msg.String() == "backspace" {
				m.step = 1
				return m, nil
			}
			dialog, selected := m.applyConfirm.Update(msg)
			m.applyConfirm = dialog
			if selected {
				if dialog.Highlighted() == ConfirmYes {
					m.step = 3
				} else {
					m.step = 1
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "up", "k":
			if m.step == 1 {
				m.moveSetup(-1)
			}
		case "down", "j":
			if m.step == 1 {
				m.moveSetup(1)
			}
		case " ":
			switch m.step {
			case 0:
				m.step = 1
			case 1:
				if m.setupActionFocused() {
					m.applyConfirm = NewConfirmDialog("Apply these settings?", ConfirmYes)
					m.step = 2
				} else {
					m.choices[m.cursor].value = !m.choices[m.cursor].value
				}
			case 3:
				m.applySetup()
			}
		case "left", "backspace":
			if m.step > 0 {
				m.step--
			}
		case "right":
			if m.step == 1 && !m.setupActionFocused() {
				m.choices[m.cursor].value = !m.choices[m.cursor].value
			}
		case "enter":
			switch m.step {
			case 0:
				m.step = 1
			case 1:
				m.applyConfirm = NewConfirmDialog("Apply these settings?", ConfirmYes)
				m.step = 2
			case 3:
				m.applySetup()
			case 4:
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
		b.WriteString("Pick the defaults you want, then apply them.\n")
		if !m.short() {
			if m.compact() {
				b.WriteString("Arrows move  •  space selects\nEnter continues from anywhere.\n\n")
			} else {
				b.WriteString("Use arrow keys to move, space to toggle or select, and enter to continue.\n\n")
			}
		}
		b.WriteString(m.actionButton("Start", true))
	case 1:
		b.WriteString(m.choicesTable(true))
		if m.short() {
			b.WriteByte('\n')
		} else {
			b.WriteString("\n\n")
			if m.compact() {
				b.WriteString(m.styles.muted.Render("↑/↓ move  •  space toggle/select\nenter continues from anywhere"))
			} else {
				b.WriteString(m.styles.muted.Render("↑/k ↓/j navigate  •  space toggle or select  •  enter continues from anywhere"))
			}
			b.WriteString("\n\n")
		}
		b.WriteString(m.actionButton("Continue", m.setupActionFocused()))
	case 2:
		b.WriteString(m.applyConfirm.View())
	case 3:
		b.WriteString("Apply setup with these choices:\n")
		if !m.short() {
			b.WriteByte('\n')
		}
		b.WriteString(m.choicesTable(false))
		if m.short() {
			b.WriteByte('\n')
		} else {
			b.WriteString("\n\n")
		}
		if m.err != nil {
			b.WriteString(m.styles.error.Render("setup failed: " + m.err.Error()))
			if m.short() {
				b.WriteByte('\n')
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(m.actionButton("Apply", true))
	case 4:
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
	return accentButtonStyle(focused).Render(buttonLabel)
}

func (m *SetupModel) applySetup() bool {
	cfg := config.Default()
	for _, choice := range m.choices {
		if choice.apply != nil {
			choice.apply(&cfg, choice.value)
		}
	}
	path := ""
	if m.save != nil {
		var err error
		path, err = m.save(cfg)
		if err != nil {
			m.err = err
			return false
		}
	}
	if m.choices[0].value && m.install != nil {
		exe := ""
		if m.exe != nil {
			resolved, err := m.exe()
			if err != nil {
				m.err = err
				return false
			}
			exe = resolved
		}
		if err := m.install(exe); err != nil {
			m.err = err
			return false
		}
	}
	m.cfg = cfg
	m.path = path
	m.err = nil
	m.applied = true
	m.step = 4
	return true
}

func (m SetupModel) summary() string {
	var b strings.Builder
	b.WriteString(m.styles.success.Render("Setup applied."))
	b.WriteString("\n\n")
	if m.path != "" {
		b.WriteString("Config: " + m.path + "\n")
	}
	if m.choices[0].value {
		b.WriteString("Autostart: installed\n")
	} else {
		b.WriteString("Autostart: skipped\n")
	}
	b.WriteString("Run now: termp start\n\n")
	b.WriteString("You can disable autostart later with `termp uninstall`; re-run setup or edit config to change these choices.\n")
	return b.String()
}
