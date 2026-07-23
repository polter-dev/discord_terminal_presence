package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

type palette struct {
	accent       lipgloss.AdaptiveColor
	success      lipgloss.AdaptiveColor
	warning      lipgloss.AdaptiveColor
	error        lipgloss.AdaptiveColor
	muted        lipgloss.AdaptiveColor
	selectedText lipgloss.AdaptiveColor
}

var defaultPalette = palette{
	accent:       lipgloss.AdaptiveColor{Light: "#6548B8", Dark: "#AB93ED"},
	success:      lipgloss.AdaptiveColor{Light: "#16733D", Dark: "#23A55A"},
	warning:      lipgloss.AdaptiveColor{Light: "#8A5A00", Dark: "#E0A82E"},
	error:        lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#ED4245"},
	muted:        lipgloss.AdaptiveColor{Light: "#5C6370", Dark: "#8B949E"},
	selectedText: lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1B1626"},
}

type styles struct {
	accentColor   string
	title         lipgloss.Style
	cursor        lipgloss.Style
	muted         lipgloss.Style
	error         lipgloss.Style
	warning       lipgloss.Style
	success       lipgloss.Style
	selected      lipgloss.Style
	path          lipgloss.Style
	focusedBorder lipgloss.Style
	mutedBorder   lipgloss.Style
}

func paletteForAccent(accentColor string) palette {
	p := defaultPalette
	switch strings.ToLower(accentColor) {
	case "blue":
		p.accent = lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#58A6FF"}
	case "green":
		p.accent = lipgloss.AdaptiveColor{Light: "#16733D", Dark: "#3FB950"}
	case "orange":
		p.accent = lipgloss.AdaptiveColor{Light: "#BC4C00", Dark: "#F0883E"}
	case "pink":
		p.accent = lipgloss.AdaptiveColor{Light: "#BF3989", Dark: "#DB61A2"}
	case "red":
		p.accent = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#FF7B72"}
	case "purple", "":
		// Preserve the original adaptive accent exactly.
	default:
		p.accent = lipgloss.AdaptiveColor{Light: accentColor, Dark: accentColor}
	}
	return p
}

func defaultStyles(accentColor string) styles {
	p := paletteForAccent(accentColor)
	return styles{
		accentColor:   accentColor,
		title:         lipgloss.NewStyle().Bold(true).Foreground(p.accent),
		cursor:        lipgloss.NewStyle().Foreground(p.accent),
		muted:         lipgloss.NewStyle().Foreground(p.muted),
		error:         lipgloss.NewStyle().Foreground(p.error),
		warning:       lipgloss.NewStyle().Foreground(p.warning),
		success:       lipgloss.NewStyle().Foreground(p.success),
		selected:      lipgloss.NewStyle().Bold(true).Foreground(p.accent),
		path:          lipgloss.NewStyle().Bold(true).Foreground(p.muted),
		focusedBorder: lipgloss.NewStyle().Foreground(p.accent),
		mutedBorder:   lipgloss.NewStyle().Foreground(p.muted),
	}
}

func accentButtonStyle(focused bool, accentColor string) lipgloss.Style {
	p := paletteForAccent(accentColor)
	style := lipgloss.NewStyle().
		Bold(true).
		Foreground(p.accent).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(p.accent).
		Padding(0, 2)
	if focused {
		style = style.Foreground(p.selectedText).Background(p.accent)
	}
	return style
}

func defaultAccentColor() string {
	return config.DefaultAccentColor
}
