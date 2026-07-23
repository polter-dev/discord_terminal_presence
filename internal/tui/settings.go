package tui

import (
	"reflect"
	"sort"
	"strings"
	"unicode"

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
	rowCategory
	rowDrill
)

type row struct {
	kind        rowKind
	label       string
	id          string
	searchTerms []string
	columnTitle string
	children    []row
	get         func(config.Config) bool
	set         func(*config.Config, bool)
	text        func(config.Config) string
	apply       func(*config.Config, string)
}

type columnKind int

const (
	columnMenu columnKind = iota
	columnSettings
	columnChoices
)

type settingsColumn struct {
	kind    columnKind
	title   string
	rows    []row
	allRows []row
	cursor  int
	offset  int
}

const maxPinResults = 6

// Model is the testable Bubble Tea settings model.
type Model struct {
	cfg           config.Config
	columns       []settingsColumn
	input         textinput.Model
	editing       int
	save          SaveFunc
	openURL       OpenURLFunc
	err           error
	status        string
	saved         bool
	saving        bool
	openingURL    bool
	quitting      bool
	confirm       *ConfirmDialog
	confirmAction rowKind
	width         int
	height        int
	styles        styles
}

type settingsSaveResultMsg struct {
	err   error
	quit  bool
	saved config.Config
}

type settingsOpenURLResultMsg struct {
	url    string
	err    error
	opened bool
}

// NewSettingsModel creates a settings model. Tools are ordered by rankedIDs first.
func NewSettingsModel(cfg config.Config, tools []registry.Tool, rankedIDs []string, save SaveFunc, openURL OpenURLFunc) Model {
	ordered := OrderToolsByUsage(tools, rankedIDs)
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 32
	input.Width = 24
	return Model{
		cfg: cfg,
		columns: []settingsColumn{{
			kind:  columnMenu,
			title: "Categories & actions",
			rows:  settingsRows(ordered),
		}},
		editing: -1,
		input:   input,
		save:    save,
		openURL: openURL,
		styles:  defaultStyles(),
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
	switch msg := msg.(type) {
	case settingsSaveResultMsg:
		m.saving = false
		if msg.err != nil {
			m.err = msg.err
			m.saved = false
			m.status = ""
			return m, nil
		}
		m.err = nil
		m.status = ""
		if !reflect.DeepEqual(m.cfg, msg.saved) {
			m.saved = false
			if msg.quit {
				return m.startSave(true)
			}
			return m, nil
		}
		m.saved = true
		if msg.quit {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case settingsOpenURLResultMsg:
		m.openingURL = false
		m.err = nil
		m.saved = false
		if msg.err != nil || !msg.opened {
			m.status = "Feedback: " + msg.url
		} else {
			m.status = "Opened feedback in your browser"
		}
		return m, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok && m.confirm != nil {
		if key.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if key.String() == "backspace" || key.String() == "esc" {
			m.confirm = nil
			return m, nil
		}
		dialog, selected := m.confirm.Update(key)
		m.confirm = &dialog
		if selected {
			action := m.confirmAction
			m.confirm = nil
			if dialog.Highlighted() == ConfirmYes {
				switch action {
				case rowSave:
					return m.startSave(true)
				case rowQuit:
					m.quitting = true
					return m, tea.Quit
				}
			}
		}
		return m, nil
	}
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
	if key, ok := msg.(tea.KeyMsg); ok && m.searching() {
		switch key.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc", "left":
			m.closeColumn()
			return m, nil
		case "up":
			m.move(-1)
			return m, nil
		case "down":
			m.move(1)
			return m, nil
		case "enter":
			return m.activate()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshPinSearch()
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureCursorsVisible()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			if len(m.columns) > 1 {
				m.closeColumn()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "left":
			m.closeColumn()
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case " ", "enter", "right":
			return m.activate()
		case "s":
			return m.startSave(false)
		}
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("termp settings"))
	b.WriteString("\n\n")
	if m.confirm != nil {
		b.WriteString(m.confirm.View())
		view := b.String()
		if m.width > 0 {
			return truncateBlock(view, m.width)
		}
		return view
	}
	b.WriteString(m.columnsView())
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(m.styles.error.Render("save failed: " + m.err.Error()))
		b.WriteByte('\n')
	} else if m.saved {
		b.WriteString("\n")
		b.WriteString(m.styles.success.Render("saved"))
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
		b.WriteString(m.styles.muted.Render(m.keybindingFooter()))
	}
	b.WriteByte('\n')
	view := b.String()
	if m.width > 0 {
		return truncateBlock(view, m.width)
	}
	return view
}

func (m Model) columnsView() string {
	columns := make([]string, len(m.columns))
	for i := range m.columns {
		columns[i] = m.settingsTable(i)
	}

	start := 0
	if m.width > 0 {
		start = len(columns) - 1
		used := lipgloss.Width(columns[start])
		for i := start - 1; i >= 0; i-- {
			candidate := lipgloss.Width(columns[i]) + 1 + used
			if candidate > m.width {
				break
			}
			start = i
			used = candidate
		}
	}

	visible := columns[start:]
	if m.width > 0 && len(visible) == 1 && lipgloss.Width(visible[0]) > m.width {
		visible[0] = truncateBlock(visible[0], m.width)
	}
	parts := make([]string, 0, len(visible)*2-1)
	for i, column := range visible {
		if i > 0 {
			parts = append(parts, " ")
		}
		parts = append(parts, column)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m Model) settingsTable(columnIndex int) string {
	column := m.columns[columnIndex]
	start, end := m.visibleRowRange(columnIndex)
	rows := make([][]string, 0, end-start+2)
	if column.kind == columnChoices {
		rows = append(rows, []string{"Search", m.input.View()})
		if len(column.rows) == 0 {
			message := "no tools available"
			if query := strings.TrimSpace(m.input.Value()); query != "" {
				message = "no tools match " + quote(query)
			}
			rows = append(rows, []string{message, ""})
		}
	}
	for i := start; i < end; i++ {
		rows = append(rows, m.rowCells(columnIndex, i, column.rows[i]))
	}

	headers := []string{"Setting", "State / Value"}
	if column.kind == columnMenu {
		headers = []string{column.title}
	} else if column.kind == columnSettings {
		headers = []string{column.title, "State / Value"}
	} else if column.kind == columnChoices {
		headers = []string{column.title, "State"}
	}
	focused := columnIndex == len(m.columns)-1
	headerStyle := m.styles.title
	borderStyle := m.styles.focusedBorder
	if !focused {
		headerStyle = m.styles.path
		borderStyle = m.styles.mutedBorder
	}

	return table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		BorderRow(false).
		StyleFunc(func(rowIndex, cellColumnIndex int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			modelRowIndex := start + rowIndex
			if column.kind == columnChoices {
				modelRowIndex--
			}
			switch {
			case rowIndex == table.HeaderRow:
				return style.Inherit(headerStyle)
			case column.kind == columnChoices && rowIndex == 0:
				return style.Inherit(m.styles.cursor)
			case column.kind == columnChoices && len(column.rows) == 0:
				return style.Inherit(m.styles.muted)
			case modelRowIndex >= 0 && modelRowIndex < len(column.rows) && cellColumnIndex == 1 && rowIsPositive(column.rows[modelRowIndex], m.cfg):
				return style.Inherit(m.styles.success)
			case modelRowIndex == column.cursor && focused:
				return style.Inherit(m.styles.selected)
			case modelRowIndex == column.cursor:
				return style.Inherit(m.styles.path)
			case !focused:
				return style.Inherit(m.styles.muted)
			default:
				return style
			}
		}).
		String()
}

func (m Model) visibleRowRange(columnIndex int) (int, int) {
	column := m.columns[columnIndex]
	if len(column.rows) == 0 {
		return 0, 0
	}

	budget := m.visibleRowBudget()
	if budget >= len(column.rows) {
		return 0, len(column.rows)
	}

	start := column.offset
	if start < 0 {
		start = 0
	}
	maxStart := len(column.rows) - budget
	if start > maxStart {
		start = maxStart
	}
	if column.cursor < start {
		start = column.cursor
	} else if column.cursor >= start+budget {
		start = column.cursor - budget + 1
	}
	if start > maxStart {
		start = maxStart
	}
	return start, start + budget
}

func (m Model) visibleRowBudget() int {
	if m.height <= 0 {
		budget := 0
		for _, column := range m.columns {
			if len(column.rows) > budget {
				budget = len(column.rows)
			}
		}
		return budget
	}

	// Reserve two title lines, four table chrome lines, one spacer, the
	// footer, and the view's trailing line. Status messages add a blank
	// line and their own line.
	fixedHeight := 2 + 4 + 1 + 2 + 1
	if m.editing >= 0 {
		fixedHeight--
	}
	if m.searching() {
		fixedHeight++
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

func (m *Model) ensureCursorsVisible() {
	for i := range m.columns {
		m.ensureCursorVisible(i)
	}
}

func (m *Model) ensureCursorVisible(columnIndex int) {
	start, _ := m.visibleRowRange(columnIndex)
	m.columns[columnIndex].offset = start
}

func (m Model) rowCells(columnIndex, index int, row row) []string {
	label := "  " + row.label
	if index == m.columns[columnIndex].cursor {
		label = "› " + row.label
	}

	if m.columns[columnIndex].kind == columnMenu {
		if row.kind == rowSave && m.saving {
			label = "  Saving…"
			if index == m.columns[columnIndex].cursor {
				label = "› Saving…"
			}
		}
		if row.kind == rowLink && m.openingURL {
			label = "  Opening…"
			if index == m.columns[columnIndex].cursor {
				label = "› Opening…"
			}
		}
		return []string{label}
	}

	switch row.kind {
	case rowToggle:
		value := "○ Off"
		if row.get(m.cfg) {
			value = "● On"
		}
		return []string{label, value}
	case rowText:
		value := row.text(m.cfg)
		if m.editing == index {
			value = m.input.View()
		}
		return []string{label, value}
	case rowPin:
		value := ""
		if m.cfg.Pin == row.id {
			value = "● Pinned"
		}
		return []string{label, value}
	case rowDrill:
		return []string{label, m.pinnedToolLabel()}
	case rowLink:
		if m.openingURL {
			return []string{label, "Opening…"}
		}
		return []string{label, "Open in browser"}
	case rowSave:
		if m.saving {
			return []string{label, "Saving…"}
		}
		return []string{label, "Write changes and exit"}
	case rowQuit:
		return []string{label, "Exit without saving"}
	default:
		return []string{label, ""}
	}
}

func rowIsPositive(row row, cfg config.Config) bool {
	switch row.kind {
	case rowToggle:
		return row.get(cfg)
	case rowPin:
		return cfg.Pin == row.id
	default:
		return false
	}
}

func (m *Model) move(delta int) {
	columnIndex := len(m.columns) - 1
	column := &m.columns[columnIndex]
	if len(column.rows) == 0 {
		return
	}
	column.cursor = (column.cursor + delta + len(column.rows)) % len(column.rows)
	m.ensureCursorVisible(columnIndex)
}

func (m Model) activate() (tea.Model, tea.Cmd) {
	columnIndex := len(m.columns) - 1
	column := m.columns[columnIndex]
	if len(column.rows) == 0 {
		return m, nil
	}
	row := column.rows[column.cursor]
	switch row.kind {
	case rowCategory:
		m.openColumn(columnSettings, row.columnTitle, row.children)
	case rowToggle:
		row.set(&m.cfg, !row.get(m.cfg))
	case rowText:
		m.editing = column.cursor
		m.input.Placeholder = ""
		m.input.SetValue(row.text(m.cfg))
		m.input.CursorEnd()
		m.input.Focus()
	case rowDrill:
		m.openColumn(columnChoices, row.columnTitle, row.children)
	case rowPin:
		if m.cfg.Pin == row.id {
			m.cfg.Pin = ""
		} else {
			m.cfg.Pin = row.id
		}
	case rowLink:
		return m.startOpenFeedback()
	case rowSave:
		if m.saving {
			return m, nil
		}
		dialog := NewConfirmDialog("Save changes and quit?", ConfirmYes)
		m.confirm = &dialog
		m.confirmAction = rowSave
	case rowQuit:
		dialog := NewConfirmDialog("Discard changes and quit?", ConfirmNo)
		m.confirm = &dialog
		m.confirmAction = rowQuit
	}
	return m, nil
}

func (m *Model) commitEdit() {
	column := m.columns[len(m.columns)-1]
	row := column.rows[m.editing]
	row.apply(&m.cfg, strings.TrimSpace(m.input.Value()))
	m.editing = -1
	m.input.Blur()
}

func (m *Model) openColumn(kind columnKind, title string, rows []row) {
	cursor := 0
	column := settingsColumn{kind: kind, title: title, rows: rows, cursor: cursor}
	if kind == columnChoices {
		m.input.Placeholder = "type to search tools…"
		m.input.PlaceholderStyle = m.styles.muted
		m.input.SetValue("")
		m.input.CursorEnd()
		m.input.Focus()
		column.allRows = rows
		column.rows = filterPinRows(rows, "")
	}
	m.columns = append(m.columns, column)
	m.ensureCursorVisible(len(m.columns) - 1)
}

func (m *Model) closeColumn() {
	if len(m.columns) <= 1 {
		return
	}
	if m.columns[len(m.columns)-1].kind == columnChoices {
		m.input.Blur()
	}
	m.columns = m.columns[:len(m.columns)-1]
}

func (m Model) searching() bool {
	return len(m.columns) > 0 && m.columns[len(m.columns)-1].kind == columnChoices
}

func (m *Model) refreshPinSearch() {
	columnIndex := len(m.columns) - 1
	column := &m.columns[columnIndex]
	column.rows = filterPinRows(column.allRows, m.input.Value())
	column.cursor = 0
	column.offset = 0
	m.ensureCursorVisible(columnIndex)
}

func (m Model) pinnedToolLabel() string {
	if m.cfg.Pin == "" {
		return "None"
	}
	for _, menuRow := range m.columns[0].rows {
		if menuRow.label != "Pin Specific Tool" {
			continue
		}
		for _, settingRow := range menuRow.children {
			for _, toolRow := range settingRow.children {
				if toolRow.id == m.cfg.Pin {
					return toolRow.label
				}
			}
		}
	}
	return m.cfg.Pin
}

func (m Model) keybindingFooter() string {
	column := m.columns[len(m.columns)-1]
	compact := m.width > 0 && m.width <= 60
	switch column.kind {
	case columnMenu:
		if compact {
			return "↑/↓ move  •  enter open\ns save  •  q/esc quit"
		}
		return "↑/k ↓/j navigate  •  enter/space/right open or activate\ns save  •  q/esc/ctrl+c quit"
	case columnChoices:
		if compact {
			return "type to search  •  ↑/↓ results\nenter pin  •  esc back  •  ctrl+c quit"
		}
		return "type to search  •  ↑/↓ navigate results  •  enter pin or unpin\nesc/left back  •  ctrl+c quit"
	default:
		if compact {
			return "↑/↓ move  •  enter change  •  esc back\ns save  •  q quit"
		}
		return "↑/k ↓/j navigate  •  enter/space/right activate, toggle, edit, or open  •  esc/left back\ns save  •  q/ctrl+c quit"
	}
}

func truncateBlock(block string, width int) string {
	return lipgloss.NewStyle().MaxWidth(width).Render(block)
}

func quote(value string) string {
	return `"` + value + `"`
}

func filterPinRows(rows []row, query string) []row {
	query = strings.TrimSpace(query)
	if query == "" {
		if len(rows) > maxPinResults {
			return append([]row(nil), rows[:maxPinResults]...)
		}
		return append([]row(nil), rows...)
	}

	type result struct {
		row   row
		score int
	}
	results := make([]result, 0, len(rows))
	for _, row := range rows {
		score, ok := pinMatchScore(query, row.searchTerms)
		if ok {
			results = append(results, result{row: row, score: score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].score < results[j].score
	})
	if len(results) > maxPinResults {
		results = results[:maxPinResults]
	}
	out := make([]row, len(results))
	for i, result := range results {
		out[i] = result.row
	}
	return out
}

func pinMatchScore(query string, terms []string) (int, bool) {
	queryParts := strings.Fields(query)
	total := 0
	for _, part := range queryParts {
		part = normalizeSearchText(part)
		if part == "" {
			continue
		}
		best := -1
		for _, term := range terms {
			for _, candidate := range searchCandidates(term) {
				if score, ok := fuzzyScore(part, candidate); ok && (best < 0 || score < best) {
					best = score
				}
			}
		}
		if best < 0 {
			return 0, false
		}
		total += best
	}
	return total, true
}

func searchCandidates(value string) []string {
	candidates := []string{normalizeSearchText(value)}
	for _, word := range strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if normalized := normalizeSearchText(word); normalized != "" {
			candidates = append(candidates, normalized)
		}
	}
	return candidates
}

func normalizeSearchText(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fuzzyScore(query, candidate string) (int, bool) {
	if query == "" || candidate == "" {
		return 0, false
	}
	if query == candidate {
		return 0, true
	}
	if strings.HasPrefix(candidate, query) {
		return 10 + len(candidate) - len(query), true
	}
	if index := strings.Index(candidate, query); index >= 0 {
		return 20 + index, true
	}
	if gaps, ok := subsequenceGaps(query, candidate); ok {
		return 40 + gaps, true
	}
	distance := levenshteinDistance(query, candidate)
	maxDistance := 1
	if len(query) >= 5 {
		maxDistance = 2
	}
	if len(query) >= 9 {
		maxDistance = 3
	}
	if distance <= maxDistance {
		return 30 + distance, true
	}
	return 0, false
}

func subsequenceGaps(query, candidate string) (int, bool) {
	queryRunes := []rune(query)
	candidateRunes := []rune(candidate)
	matched := 0
	first := -1
	last := -1
	for i, r := range candidateRunes {
		if matched < len(queryRunes) && r == queryRunes[matched] {
			if first < 0 {
				first = i
			}
			last = i
			matched++
		}
	}
	if matched != len(queryRunes) {
		return 0, false
	}
	return last - first + 1 - len(queryRunes), true
}

func levenshteinDistance(left, right string) int {
	a := []rune(left)
	b := []rune(right)
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, ar := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, br := range b {
			cost := 0
			if ar != br {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

func (m Model) startSave(quit bool) (tea.Model, tea.Cmd) {
	if m.saving {
		return m, nil
	}
	m.saving = true
	m.err = nil
	m.saved = false
	m.status = ""
	cfg := m.cfg
	save := m.save
	return m, func() tea.Msg {
		var err error
		if save != nil {
			err = save(cfg)
		}
		return settingsSaveResultMsg{err: err, quit: quit, saved: cfg}
	}
}

func (m Model) startOpenFeedback() (tea.Model, tea.Cmd) {
	if m.openingURL {
		return m, nil
	}
	url := strings.TrimSpace(m.cfg.FeedbackURL)
	if url == "" {
		url = config.DefaultFeedbackURL
	}
	m.err = nil
	m.saved = false
	m.openingURL = true
	openURL := m.openURL
	return m, func() tea.Msg {
		if openURL == nil {
			return settingsOpenURLResultMsg{url: url}
		}
		err := openURL(url)
		return settingsOpenURLResultMsg{url: url, err: err, opened: err == nil}
	}
}

func settingsRows(tools []registry.Tool) []row {
	global := []row{
		toggle("Presence enabled", func(c config.Config) bool { return c.Enabled }, func(c *config.Config, v bool) { c.Enabled = v }),
		text("Scan interval", func(c config.Config) string { return c.ScanInterval }, func(c *config.Config, v string) { c.ScanInterval = v }),
		toggle("Automatic updates", func(c config.Config) bool { return c.AutoUpdate }, func(c *config.Config, v bool) { c.AutoUpdate = v }),
	}
	display := []row{
		toggle("Tool name", func(c config.Config) bool { return c.Display.ToolName }, func(c *config.Config, v bool) { c.Display.ToolName = v }),
		toggle("Elapsed timer", func(c config.Config) bool { return c.Display.ElapsedTimer }, func(c *config.Config, v bool) { c.Display.ElapsedTimer = v }),
		toggle("Small image", func(c config.Config) bool { return c.Display.SmallImage }, func(c *config.Config, v bool) { c.Display.SmallImage = v }),
		toggle("Buttons", func(c config.Config) bool { return c.Display.Buttons }, func(c *config.Config, v bool) { c.Display.Buttons = v }),
		toggle("Collection label", func(c config.Config) bool { return c.Display.Collection }, func(c *config.Config, v bool) { c.Display.Collection = v }),
	}
	privacy := []row{
		toggle("Show folder", func(c config.Config) bool { return c.Privacy.ShowDirectory }, func(c *config.Config, v bool) { c.Privacy.ShowDirectory = v }),
		toggle("Folder: name only", func(c config.Config) bool { return c.Privacy.DirectoryBasenameOnly }, func(c *config.Config, v bool) { c.Privacy.DirectoryBasenameOnly = v }),
	}
	headliner := []row{
		toggle("Activity switching", func(c config.Config) bool { return c.ActivitySwitching }, func(c *config.Config, v bool) { c.ActivitySwitching = v }),
		text("Spotlight idle timeout", func(c config.Config) string { return c.HeadlinerIdleTimeout }, func(c *config.Config, v string) { c.HeadlinerIdleTimeout = v }),
	}
	pinChoices := make([]row, 0, len(tools))
	for _, tool := range tools {
		label := tool.DisplayName
		if label == "" {
			label = tool.ID
		}
		pinChoices = append(pinChoices, row{
			kind:        rowPin,
			label:       label,
			id:          tool.ID,
			searchTerms: []string{label, tool.ID, tool.Match.Name},
		})
	}

	return []row{
		category("Global", global),
		category("Display", display),
		category("Privacy", privacy),
		category("Headliner", headliner),
		category("Pin Specific Tool", []row{{
			kind:        rowDrill,
			label:       "Pinned tool",
			columnTitle: "Choose a tool",
			children:    pinChoices,
		}}),
		row{kind: rowLink, label: "Leave feedback"},
		row{kind: rowSave, label: "Save & quit"},
		row{kind: rowQuit, label: "Quit without saving"},
	}
}

func category(label string, rows []row) row {
	return row{kind: rowCategory, label: label, columnTitle: label, children: rows}
}

func toggle(label string, get func(config.Config) bool, set func(*config.Config, bool)) row {
	return row{kind: rowToggle, label: label, get: get, set: set}
}

func text(label string, get func(config.Config) string, set func(*config.Config, string)) row {
	return row{kind: rowText, label: label, text: get, apply: set}
}
