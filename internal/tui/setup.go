package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	choices []setupChoice
	cursor  int
	step    int
	save    SetupSaveFunc
	install SetupInstallFunc
	exe     SetupExeFunc
	cfg     config.Config
	path    string
	err     error
	applied bool
	styles  styles
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
				value: true,
			},
			{
				label: "Show your working directory on Discord?",
				value: cfg.Privacy.ShowDirectory,
				apply: func(c *config.Config, v bool) {
					c.Privacy.ShowDirectory = v
				},
			},
			{
				label: "Show the 'What is this?' button?",
				value: cfg.CTA.Enabled,
				apply: func(c *config.Config, v bool) {
					c.CTA.Enabled = v
				},
			},
		},
		styles: styles{
			title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
			cursor:   lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
			muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
			error:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
			selected: lipgloss.NewStyle().Bold(true),
		},
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
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.step == 1 {
				m.moveSetup(-1)
			}
		case "down", "j":
			if m.step == 1 {
				m.moveSetup(1)
			}
		case " ", "left", "right":
			if m.step == 1 {
				m.choices[m.cursor].value = !m.choices[m.cursor].value
			}
		case "enter":
			switch m.step {
			case 0:
				m.step = 1
			case 1:
				m.step = 2
			case 2:
				m.applySetup()
			case 3:
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m SetupModel) View() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("termp setup"))
	b.WriteString("\n\n")
	switch m.step {
	case 0:
		b.WriteString("Pick the defaults you want, then apply them.\n")
		b.WriteString("Use arrow keys to move, space to toggle, and enter to continue.\n\n")
		b.WriteString(m.styles.selected.Render("Start"))
	case 1:
		for i, choice := range m.choices {
			prefix := "  "
			if i == m.cursor {
				prefix = m.styles.cursor.Render("> ")
			}
			mark := "[ ]"
			if choice.value {
				mark = "[x]"
			}
			b.WriteString(prefix + mark + " " + choice.label + "\n")
		}
		b.WriteString("\n")
		b.WriteString(m.styles.muted.Render("Enter continues to Apply."))
	case 2:
		b.WriteString("Apply setup with these choices:\n\n")
		for _, choice := range m.choices {
			value := "no"
			if choice.value {
				value = "yes"
			}
			b.WriteString(fmt.Sprintf("- %s %s\n", choice.label, value))
		}
		b.WriteString("\n")
		if m.err != nil {
			b.WriteString(m.styles.error.Render("setup failed: " + m.err.Error()))
			b.WriteByte('\n')
		}
		b.WriteString(m.styles.selected.Render("Apply"))
	case 3:
		b.WriteString(m.summary())
	}
	return b.String()
}

func (m *SetupModel) moveSetup(delta int) {
	m.cursor = (m.cursor + delta + len(m.choices)) % len(m.choices)
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
	m.step = 3
	return true
}

func (m SetupModel) summary() string {
	var b strings.Builder
	b.WriteString("Setup applied.\n\n")
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
