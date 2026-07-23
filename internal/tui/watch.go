package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
)

const maxRecentDetections = 5

// ActivityMsg carries a config-resolved activity into the watch TUI.
type ActivityMsg struct {
	Activity     *presence.Activity
	FeaturedName string
}

// ConnMsg reports whether Discord IPC is reachable.
type ConnMsg bool

type tickMsg time.Time

// WatchModel is the testable Bubble Tea model for termp watch.
type WatchModel struct {
	activity     *presence.Activity
	connected    bool
	recent       []RecentDetection
	lastFeatured string
	now          time.Time
	nowFunc      func() time.Time
	quitting     bool
	styles       CardStyles
}

// NewWatchModel creates a watch model using the real clock.
func NewWatchModel() WatchModel {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	return newWatchModel(cfg.UI.AccentColor, time.Now)
}

// NewWatchModelWithClock creates a watch model with an injected clock.
func NewWatchModelWithClock(nowFunc func() time.Time) WatchModel {
	return newWatchModel(defaultAccentColor(), nowFunc)
}

// NewWatchModelWithConfig creates a watch model styled from cfg.
func NewWatchModelWithConfig(cfg config.Config, nowFunc func() time.Time) WatchModel {
	return newWatchModel(cfg.UI.AccentColor, nowFunc)
}

func newWatchModel(accentColor string, nowFunc func() time.Time) WatchModel {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return WatchModel{
		now:     nowFunc(),
		nowFunc: nowFunc,
		styles:  DefaultCardStyles(accentColor),
	}
}

// CurrentActivity returns the currently displayed activity.
func (m WatchModel) CurrentActivity() *presence.Activity {
	return m.activity
}

// Connected reports the latest Discord connection state.
func (m WatchModel) Connected() bool {
	return m.connected
}

// Recent returns a copy of the recent detections list.
func (m WatchModel) Recent() []RecentDetection {
	return append([]RecentDetection(nil), m.recent...)
}

// Quitting reports whether the model handled a quit key.
func (m WatchModel) Quitting() bool {
	return m.quitting
}

func (m WatchModel) Init() tea.Cmd {
	return tickCmd()
}

func (m WatchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ActivityMsg:
		m.activity = msg.Activity
		name := msg.FeaturedName
		if name == "" && msg.Activity != nil {
			name = msg.Activity.LargeImage.Text
		}
		if name != "" && name != m.lastFeatured {
			m.pushRecent(name)
			m.lastFeatured = name
		}
		if msg.Activity == nil {
			m.lastFeatured = ""
		}
	case ConnMsg:
		m.connected = bool(msg)
	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m WatchModel) View() string {
	var b strings.Builder
	b.WriteString(m.styles.Title.Render("termp watch - live Discord preview"))
	b.WriteString("\n\n")
	b.WriteString(RenderCard(CardState{
		Activity:  m.activity,
		Connected: m.connected,
		Now:       m.now,
		Recent:    m.recent,
	}, m.styles))
	b.WriteString("\n\n")
	b.WriteString(m.styles.Muted.Render("press q to quit"))
	return b.String()
}

func (m *WatchModel) pushRecent(name string) {
	m.recent = append([]RecentDetection{{Name: name, At: m.now}}, m.recent...)
	if len(m.recent) > maxRecentDetections {
		m.recent = m.recent[:maxRecentDetections]
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
