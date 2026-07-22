package tui

import "github.com/charmbracelet/lipgloss"

type palette struct {
	accent       lipgloss.AdaptiveColor
	success      lipgloss.AdaptiveColor
	warning      lipgloss.AdaptiveColor
	error        lipgloss.AdaptiveColor
	muted        lipgloss.AdaptiveColor
	selectedText lipgloss.AdaptiveColor
}

var tuiPalette = palette{
	accent:       lipgloss.AdaptiveColor{Light: "#6548B8", Dark: "#AB93ED"},
	success:      lipgloss.AdaptiveColor{Light: "#16733D", Dark: "#23A55A"},
	warning:      lipgloss.AdaptiveColor{Light: "#8A5A00", Dark: "#E0A82E"},
	error:        lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#ED4245"},
	muted:        lipgloss.AdaptiveColor{Light: "#5C6370", Dark: "#8B949E"},
	selectedText: lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1B1626"},
}

type styles struct {
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

func defaultStyles() styles {
	return styles{
		title:         lipgloss.NewStyle().Bold(true).Foreground(tuiPalette.accent),
		cursor:        lipgloss.NewStyle().Foreground(tuiPalette.accent),
		muted:         lipgloss.NewStyle().Foreground(tuiPalette.muted),
		error:         lipgloss.NewStyle().Foreground(tuiPalette.error),
		warning:       lipgloss.NewStyle().Foreground(tuiPalette.warning),
		success:       lipgloss.NewStyle().Foreground(tuiPalette.success),
		selected:      lipgloss.NewStyle().Bold(true).Foreground(tuiPalette.accent),
		path:          lipgloss.NewStyle().Bold(true).Foreground(tuiPalette.muted),
		focusedBorder: lipgloss.NewStyle().Foreground(tuiPalette.accent),
		mutedBorder:   lipgloss.NewStyle().Foreground(tuiPalette.muted),
	}
}

func accentButtonStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().
		Bold(true).
		Foreground(tuiPalette.accent).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiPalette.accent).
		Padding(0, 2)
	if focused {
		style = style.Foreground(tuiPalette.selectedText).Background(tuiPalette.accent)
	}
	return style
}
