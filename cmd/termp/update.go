package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

const updateCheckTimeout = 2 * time.Second

var releaseChecker = updatepkg.NewChecker(nil, updatepkg.DefaultCachePath())

func printAvailableUpdate(cfg config.Config, loadErr error) {
	// A malformed config may contain an opt-out we cannot safely read. Privacy
	// wins over checking in that case.
	if loadErr != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	result, ok := releaseChecker.Check(ctx, version, cfg.UpdateCheck)
	if !ok {
		return
	}
	fmt.Print(formatUpdateNotice(
		result,
		installRenderer(os.Stdout),
		installOutputWidth(os.Stdout),
	))
}

func formatUpdateNotice(result updatepkg.Result, renderer *lipgloss.Renderer, width int) string {
	width = min(max(width, 20), maxInstallCTAWidth)

	style := func(value string, styled lipgloss.Style) string {
		if renderer == nil {
			return value
		}
		return styled.Render(value)
	}
	newStyle := func() lipgloss.Style {
		if renderer == nil {
			return lipgloss.NewStyle()
		}
		return renderer.NewStyle()
	}

	headerStyle := newStyle().Foreground(lipgloss.Color("11")).Bold(true)
	commandStyle := newStyle().Foreground(lipgloss.Color("14"))
	header := fmt.Sprintf("Update available: %s (current %s)", result.Latest, result.Current)

	lines := wrapOutputText(header, width)
	for i := range lines {
		lines[i] = style(lines[i], headerStyle)
	}
	lines = append(lines, "Run:")
	commandLines := wrapShellCommand(result.Command, width)
	for _, line := range commandLines {
		lines = append(lines, style(line, commandStyle))
	}
	return strings.Join(lines, "\n") + "\n"
}

func wrapOutputText(value string, width int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	line := ""
	for _, word := range words {
		if line != "" && len(line)+1+len(word) <= width {
			line += " " + word
			continue
		}
		if line != "" {
			lines = append(lines, line)
			line = ""
		}
		for len(word) > width {
			lines = append(lines, word[:width])
			word = word[width:]
		}
		line = word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// wrapShellCommand keeps each physical line within width while preserving a
// copy-pasteable shell command through backslash-newline continuations.
func wrapShellCommand(command string, width int) []string {
	width = max(width, 4)
	limit := width - 2 // reserve " \\" when another argument follows
	var lines []string
	line := ""
	for _, token := range strings.Fields(command) {
		if line != "" && len(line)+1+len(token) <= limit {
			line += " " + token
			continue
		}
		if line != "" {
			lines = append(lines, line+" \\")
			line = ""
		}
		for len(token) > limit {
			lines = append(lines, token[:width-1]+"\\")
			token = token[width-1:]
		}
		line = token
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
