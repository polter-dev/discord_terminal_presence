package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// SaveFunc persists a settings config.
type SaveFunc func(config.Config) error

// OpenURLFunc opens a URL outside the TUI.
type OpenURLFunc func(string) error

type rowKind int

const (
	rowToggle rowKind = iota
	rowText
	rowPin
	rowLink
	rowSave
	rowQuit
	rowLabel
)

type row struct {
	kind  rowKind
	label string
	id    string
	get   func(config.Config) bool
	set   func(*config.Config, bool)
	text  func(config.Config) string
	apply func(*config.Config, string)
}

// Model is the testable Bubble Tea settings model.
type Model struct {
	cfg      config.Config
	rows     []row
	cursor   int
	input    textinput.Model
	editing  int
	save     SaveFunc
	openURL  OpenURLFunc
	err      error
	status   string
	saved    bool
	quitting bool
	width    int
	height   int
	offset   int
	styles   styles
}

type styles struct {
	title    lipgloss.Style
	cursor   lipgloss.Style
	muted    lipgloss.Style
	error    lipgloss.Style
	selected lipgloss.Style
}

// NewSettingsModel creates a settings model. Tools are ordered by rankedIDs first.
func NewSettingsModel(cfg config.Config, tools []registry.Tool, rankedIDs []string, save SaveFunc, openURL OpenURLFunc) Model {
	ordered := OrderToolsByUsage(tools, rankedIDs)
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 32
	input.Width = 24
	return Model{
		cfg:     cfg,
		rows:    settingsRows(ordered),
		cursor:  1,
		editing: -1,
		input:   input,
		save:    save,
		openURL: openURL,
		styles: styles{
			title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
			cursor:   lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
			muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
			error:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
			selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
		},
	}
}

// OrderToolsByUsage returns tools ranked by usage first, then by display name.
func OrderToolsByUsage(tools []registry.Tool, rankedIDs []string) []registry.Tool {
	byID := make(map[string]registry.Tool, len(tools))
	for _, tool := range tools {
		byID[tool.ID] = tool
	}
	out := make([]registry.Tool, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, id := range rankedIDs {
		if tool, ok := byID[id]; ok {
			out = append(out, tool)
			seen[id] = true
		}
	}
	remaining := make([]registry.Tool, 0, len(tools)-len(out))
	for _, tool := range tools {
		if !seen[tool.ID] {
			remaining = append(remaining, tool)
		}
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		left := strings.ToLower(remaining[i].DisplayName)
		right := strings.ToLower(remaining[j].DisplayName)
		if left != right {
			return left < right
		}
		return remaining[i].ID < remaining[j].ID
	})
	return append(out, remaining...)
}

// Config returns the model's current config.
func (m Model) Config() config.Config {
	return m.cfg
}

// Saved reports whether the model successfully saved.
func (m Model) Saved() bool {
	return m.saved
}

// Err returns the latest save error.
func (m Model) Err() error {
	return m.err
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && m.editing >= 0 {
		switch key.String() {
		case "enter":
			m.commitEdit()
			return m, nil
		case "esc":
			m.editing = -1
			m.input.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureCursorVisible()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case " ", "enter":
			return m.activate()
		case "s":
			m.saveConfig()
		}
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("termp settings"))
	b.WriteString("\n\n")
	start, end := m.visibleRowRange()
	b.WriteString(m.settingsTable(start, end))
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(m.styles.error.Render("save failed: " + m.err.Error()))
		b.WriteByte('\n')
	} else if m.saved {
		b.WriteString("\n")
		b.WriteString(m.styles.muted.Render("saved"))
		b.WriteByte('\n')
	} else if m.status != "" {
		b.WriteString("\n")
		b.WriteString(m.styles.muted.Render(m.status))
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	if m.editing >= 0 {
		b.WriteString(m.styles.muted.Render("type to edit  •  enter apply  •  esc cancel"))
	} else {
		b.WriteString(m.styles.muted.Render("↑/k ↓/j navigate  •  enter/space activate, toggle, or edit\ns save  •  q/esc/ctrl+c quit"))
	}
	b.WriteByte('\n')
	return b.String()
}

func (m Model) settingsTable(start, end int) string {
	rows := make([][]string, 0, end-start)
	for i := start; i < end; i++ {
		row := m.rows[i]
		rows = append(rows, m.rowCells(i, row))
	}

	return table.New().
		Headers("Setting", "State / Value").
		Rows(rows...).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(m.styles.muted).
		BorderRow(false).
		StyleFunc(func(rowIndex, _ int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			modelRowIndex := start + rowIndex
			switch {
			case rowIndex == table.HeaderRow:
				return style.Inherit(m.styles.title)
			case m.rows[modelRowIndex].kind == rowLabel:
				return style.Inherit(m.styles.title)
			case modelRowIndex == m.cursor:
				return style.Inherit(m.styles.selected)
			default:
				return style
			}
		}).
		String()
}

func (m Model) visibleRowRange() (int, int) {
	if len(m.rows) == 0 {
		return 0, 0
	}

	budget := m.visibleRowBudget()
	if budget >= len(m.rows) {
		return 0, len(m.rows)
	}

	start := m.offset
	if start < 0 {
		start = 0
	}
	maxStart := len(m.rows) - budget
	if start > maxStart {
		start = maxStart
	}
	if m.cursor < start {
		start = m.cursor
	} else if m.cursor >= start+budget {
		start = m.cursor - budget + 1
	}
	if start > maxStart {
		start = maxStart
	}
	return start, start + budget
}

func (m Model) visibleRowBudget() int {
	if m.height <= 0 {
		return len(m.rows)
	}

	// Reserve two title lines, four table chrome lines, one spacer, the
	// footer, and the view's trailing line. Status messages add a blank
	// line and their own line.
	fixedHeight := 2 + 4 + 1 + 2 + 1
	if m.editing >= 0 {
		fixedHeight--
	}
	if m.err != nil || m.saved || m.status != "" {
		fixedHeight += 2
	}
	budget := m.height - fixedHeight
	if budget < 1 {
		return 1
	}
	return budget
}

func (m *Model) ensureCursorVisible() {
	start, _ := m.visibleRowRange()
	m.offset = start
}

func (m Model) rowCells(index int, row row) []string {
	label := "  " + row.label
	if index == m.cursor {
		label = "› " + row.label
	}

	switch row.kind {
	case rowLabel:
		return []string{"  " + row.label, ""}
	case rowToggle:
		value := "Off"
		if row.get(m.cfg) {
			value = "On"
		}
		return []string{label, value}
	case rowText:
		value := row.text(m.cfg)
		if m.editing == index {
			value = m.input.View()
		}
		return []string{label, value}
	case rowPin:
		value := "—"
		if m.cfg.Pin == row.id {
			value = "Pinned"
		}
		return []string{label, value}
	case rowLink:
		return []string{label, "Open in browser"}
	case rowSave:
		return []string{label, "Write changes and exit"}
	case rowQuit:
		return []string{label, "Exit without saving"}
	default:
		return []string{label, ""}
	}
}

func (m *Model) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	for {
		m.cursor = (m.cursor + delta + len(m.rows)) % len(m.rows)
		if m.rows[m.cursor].kind != rowLabel {
			m.ensureCursorVisible()
			return
		}
	}
}

func (m Model) activate() (tea.Model, tea.Cmd) {
	row := m.rows[m.cursor]
	switch row.kind {
	case rowToggle:
		row.set(&m.cfg, !row.get(m.cfg))
	case rowText:
		m.editing = m.cursor
		m.input.SetValue(row.text(m.cfg))
		m.input.CursorEnd()
		m.input.Focus()
	case rowPin:
		if m.cfg.Pin == row.id {
			m.cfg.Pin = ""
		} else {
			m.cfg.Pin = row.id
		}
	case rowLink:
		m.openFeedback()
	case rowSave:
		if m.saveConfig() {
			m.quitting = true
			return m, tea.Quit
		}
	case rowQuit:
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) commitEdit() {
	row := m.rows[m.editing]
	row.apply(&m.cfg, strings.TrimSpace(m.input.Value()))
	m.editing = -1
	m.input.Blur()
}

func (m *Model) saveConfig() bool {
	if m.save == nil {
		m.saved = true
		m.err = nil
		m.status = ""
		return true
	}
	if err := m.save(m.cfg); err != nil {
		m.err = err
		m.saved = false
		m.status = ""
		return false
	}
	m.err = nil
	m.saved = true
	m.status = ""
	return true
}

func (m *Model) openFeedback() {
	url := strings.TrimSpace(m.cfg.FeedbackURL)
	if url == "" {
		url = config.DefaultFeedbackURL
	}
	m.err = nil
	m.saved = false
	if m.openURL == nil {
		m.status = "Feedback: " + url
		return
	}
	if err := m.openURL(url); err != nil {
		m.status = "Feedback: " + url
		return
	}
	m.status = "Opened feedback in your browser"
}

func settingsRows(tools []registry.Tool) []row {
	rows := []row{
		{kind: rowLabel, label: "Global"},
		toggle("Presence enabled", func(c config.Config) bool { return c.Enabled }, func(c *config.Config, v bool) { c.Enabled = v }),
		text("Scan interval", func(c config.Config) string { return c.ScanInterval }, func(c *config.Config, v string) { c.ScanInterval = v }),
		{kind: rowLabel, label: "Display"},
		toggle("Tool name", func(c config.Config) bool { return c.Display.ToolName }, func(c *config.Config, v bool) { c.Display.ToolName = v }),
		toggle("Elapsed timer", func(c config.Config) bool { return c.Display.ElapsedTimer }, func(c *config.Config, v bool) { c.Display.ElapsedTimer = v }),
		toggle("Small image", func(c config.Config) bool { return c.Display.SmallImage }, func(c *config.Config, v bool) { c.Display.SmallImage = v }),
		toggle("Buttons", func(c config.Config) bool { return c.Display.Buttons }, func(c *config.Config, v bool) { c.Display.Buttons = v }),
		toggle("Collection label", func(c config.Config) bool { return c.Display.Collection }, func(c *config.Config, v bool) { c.Display.Collection = v }),
		{kind: rowLabel, label: "Privacy"},
		toggle("Show folder", func(c config.Config) bool { return c.Privacy.ShowDirectory }, func(c *config.Config, v bool) { c.Privacy.ShowDirectory = v }),
		toggle("Folder: name only", func(c config.Config) bool { return c.Privacy.DirectoryBasenameOnly }, func(c *config.Config, v bool) { c.Privacy.DirectoryBasenameOnly = v }),
		{kind: rowLabel, label: "Headliner"},
		toggle("Activity switching", func(c config.Config) bool { return c.ActivitySwitching }, func(c *config.Config, v bool) { c.ActivitySwitching = v }),
		text("Spotlight idle timeout", func(c config.Config) string { return c.HeadlinerIdleTimeout }, func(c *config.Config, v string) { c.HeadlinerIdleTimeout = v }),
		{kind: rowLabel, label: "Pin Specific Tool"},
	}
	for _, tool := range tools {
		label := tool.DisplayName
		if label == "" {
			label = tool.ID
		}
		rows = append(rows, row{kind: rowPin, label: label, id: tool.ID})
	}
	rows = append(rows,
		row{kind: rowLink, label: "Leave feedback"},
		row{kind: rowSave, label: "Save & quit"},
		row{kind: rowQuit, label: "Quit without saving"},
	)
	return rows
}

func toggle(label string, get func(config.Config) bool, set func(*config.Config, bool)) row {
	return row{kind: rowToggle, label: label, get: get, set: set}
}

func text(label string, get func(config.Config) string, set func(*config.Config, string)) row {
	return row{kind: rowText, label: label, text: get, apply: set}
}
