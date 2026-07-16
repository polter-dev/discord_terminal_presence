package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	xterm "github.com/charmbracelet/x/term"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
)

const maxInstallCTAWidth = 80

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	force := fs.Bool("force", false, "install even when the executable path is unstable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := service.ResolveExecutable()
	if err != nil {
		return err
	}
	exe, err = service.ValidateInstallExecutable(exe, *force)
	if err != nil {
		return err
	}
	state, err := service.NewManager().Install(exe)
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	fmt.Print(formatInstallSuccess(
		state.Path,
		config.DefaultPath(),
		installRenderer(os.Stdout),
		installOutputWidth(os.Stdout),
	))
	return nil
}

func formatInstallSuccess(installedPath, configPath string, renderer *lipgloss.Renderer, width int) string {
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Sprintf("installed: %s\nruns: termp start\nundo: termp uninstall\n", installedPath)
	}

	return fmt.Sprintf("installed: %s\n%s", installedPath, renderInstallCTA(renderer, width))
}

func installRenderer(output *os.File) *lipgloss.Renderer {
	_, noColor := os.LookupEnv("NO_COLOR")
	return newInstallRenderer(output, xterm.IsTerminal(output.Fd()), noColor)
}

func newInstallRenderer(output *os.File, terminal, noColor bool) *lipgloss.Renderer {
	if !terminal || noColor {
		return nil
	}
	return lipgloss.NewRenderer(output)
}

func installOutputWidth(output *os.File) int {
	width, _, err := xterm.GetSize(output.Fd())
	if err != nil || width <= 0 {
		width, _ = strconv.Atoi(os.Getenv("COLUMNS"))
		if width <= 0 {
			return maxInstallCTAWidth
		}
	}
	return min(width, maxInstallCTAWidth)
}

func renderInstallCTA(renderer *lipgloss.Renderer, width int) string {
	width = min(max(width, len("| termp setup |")), maxInstallCTAWidth)
	padding := 3
	if width < 40 {
		padding = 1
	}
	contentWidth := width - 2 - padding*2

	style := func(s lipgloss.Style, text string) string {
		if renderer == nil {
			return text
		}
		return s.Render(text)
	}
	newStyle := func() lipgloss.Style {
		if renderer == nil {
			return lipgloss.NewStyle()
		}
		return renderer.NewStyle()
	}

	borderStyle := newStyle().Foreground(lipgloss.Color("13")).Bold(true)
	headerStyle := newStyle().Foreground(lipgloss.Color("14")).Bold(true)
	nextStyle := newStyle().Foreground(lipgloss.Color("11")).Bold(true)
	commandStyle := newStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("11")).
		Bold(true)
	consequenceStyle := newStyle().Foreground(lipgloss.Color("15")).Bold(true)
	secondaryStyle := newStyle().Foreground(lipgloss.Color("8")).Faint(true)

	border := func(text string) string { return style(borderStyle, text) }
	boxLine := func(text string) string {
		remaining := max(contentWidth-lipgloss.Width(text), 0)
		return border("|") + strings.Repeat(" ", padding) + text + strings.Repeat(" ", remaining+padding) + border("|")
	}
	blankLine := boxLine("")

	var lines []string
	lines = append(lines, border("+"+strings.Repeat("-", width-2)+"+"), blankLine)
	for _, line := range wrapInstallText("TERMP INSTALLED", contentWidth) {
		lines = append(lines, boxLine(style(headerStyle, line)))
	}
	lines = append(lines, blankLine)
	next := "NEXT STEP - RUN THIS NOW:"
	for _, line := range wrapInstallText(next, contentWidth) {
		lines = append(lines, boxLine(style(nextStyle, line)))
	}
	lines = append(lines, blankLine)
	command := "termp setup"
	if contentWidth >= len(">>>  termp setup  <<<") {
		command = ">>>  termp setup  <<<"
	}
	if renderer != nil {
		command = commandStyle.Width(contentWidth).Align(lipgloss.Center).Render(command)
	} else {
		command = centerInstallText(command, contentWidth)
	}
	lines = append(lines, boxLine(command), blankLine)
	for _, line := range wrapInstallText("Nothing shows on your Discord profile until you do.", contentWidth) {
		lines = append(lines, boxLine(style(consequenceStyle, line)))
	}
	lines = append(lines, blankLine, border("+"+strings.Repeat("-", width-2)+"+"))

	secondary := []string{"optional: termp start", "remove:   termp uninstall"}
	if width < len(secondary[1]) {
		secondary = []string{"termp start", "termp uninstall"}
	}
	for i := range secondary {
		secondary[i] = style(secondaryStyle, secondary[i])
	}

	return "\n\n" + strings.Join(lines, "\n") + "\n\n\n" + strings.Join(secondary, "\n") + "\n"
}

func wrapInstallText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	lines := []string{words[0]}
	for _, word := range words[1:] {
		last := len(lines) - 1
		if len(lines[last])+1+len(word) <= width {
			lines[last] += " " + word
		} else {
			lines = append(lines, word)
		}
	}
	return lines
}

func centerInstallText(text string, width int) string {
	space := max(width-len(text), 0)
	return strings.Repeat(" ", space/2) + text + strings.Repeat(" ", space-space/2)
}

func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := service.NewManager().Uninstall()
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if state.Path == "" {
		fmt.Println("not installed")
		return nil
	}
	fmt.Printf("removed: %s\n", state.Path)
	return nil
}

func disable(args []string) error {
	fs := flag.NewFlagSet("disable", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := service.NewManager().Disable()
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if !state.Installed {
		fmt.Println("autostart not installed (nothing to disable); run: termp stop")
		return nil
	}
	pidPath := pidFilePath()
	if pid, err := readPID(pidPath); err == nil && !processAlive(pid) {
		removePID(pidPath)
	}
	fmt.Println("disabled: autostart paused (re-enable with: termp enable)")
	return nil
}

func enable(args []string) error {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := service.NewManager().Enable()
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if !state.Installed {
		fmt.Println("autostart not installed; run: termp install")
		return nil
	}
	fmt.Println("enabled: autostart active")
	return nil
}
