package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

const updateCheckTimeout = 2 * time.Second
const automaticUpdateTimeout = 5 * time.Minute

var releaseChecker = updatepkg.NewChecker(nil, updatepkg.DefaultCachePath())

type latestChecker interface {
	Latest(context.Context, string) (updatepkg.Result, error)
}

type automaticUpdateChecker interface {
	Check(context.Context, string, bool) (updatepkg.Result, bool)
}

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

func printCommandUpdateAlert(command string, args []string, interactive bool, cfg config.Config, loadErr error, stderr io.Writer) {
	if loadErr != nil || cfg.AutoUpdate || !eligibleForUpdateAlert(command, args, interactive) {
		return
	}
	result, ok := releaseChecker.CachedCheck(version, cfg.UpdateCheck)
	if !ok {
		return
	}
	fmt.Fprintf(stderr, "A new version (%s) is available — run `termp update`\n", result.Latest)
}

// runAutomaticUpdate is fail-open: startup callers never receive an update
// error. They run it asynchronously so even a slow package manager cannot
// delay the daemon. The installed release is used on the next daemon start.
func runAutomaticUpdate(ctx context.Context, cfg config.Config, current string, checker automaticUpdateChecker, runner updatepkg.CommandRunner) {
	if !cfg.AutoUpdate || !cfg.UpdateCheck || checker == nil {
		return
	}
	checkCtx, cancelCheck := context.WithTimeout(ctx, updateCheckTimeout)
	result, ok := checker.Check(checkCtx, current, cfg.UpdateCheck)
	cancelCheck()
	if !ok {
		debugf("automatic update check skipped or found no newer release")
		return
	}

	updateCtx, cancelUpdate := context.WithTimeout(ctx, automaticUpdateTimeout)
	defer cancelUpdate()
	// Homebrew owns Homebrew-installed binaries, so PerformUpdate delegates to
	// `brew upgrade --cask` instead of replacing the executable directly.
	if err := updatepkg.PerformUpdate(updateCtx, result.Method, result.Latest, runner, nil, io.Discard, io.Discard); err != nil {
		debugf("automatic update skipped: %v", err)
		return
	}
	debugf("automatic update installed %s; it will take effect on next start", result.Latest)
}

func eligibleForUpdateAlert(command string, args []string, interactive bool) bool {
	switch command {
	case "install", "uninstall", "disable", "enable", "start", "stop":
		return true
	case "settings", "setup":
		return interactive
	case "watch":
		if !interactive {
			return false
		}
		for _, arg := range args {
			if arg == "--once" {
				return false
			}
		}
		return true
	default:
		// update, version, and status report update state themselves. config and
		// completion are intended for non-interactive/script consumption.
		return false
	}
}

func updateCommand(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: termp update")
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return runUpdate(checkCtx, context.Background(), version, releaseChecker, updatepkg.ExecRunner{}, os.Stdin, os.Stdout, os.Stderr)
}

func runUpdate(checkCtx, updateCtx context.Context, current string, checker latestChecker, runner updatepkg.CommandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	result, err := checker.Latest(checkCtx, current)
	if err != nil {
		return fmt.Errorf("unable to check for updates: %w", err)
	}
	if !updatepkg.IsNewer(current, result.Latest) {
		fmt.Fprintf(stdout, "You're already on the latest version (%s).\n", result.Latest)
		return nil
	}
	fmt.Fprintf(stdout, "Updating termp from %s to %s...\n", current, result.Latest)
	// Generic installs use the exact-tag installer, which verifies the release
	// checksum's keyless signature before replacing the binary.
	return updatepkg.PerformUpdate(updateCtx, result.Method, result.Latest, runner, stdin, stdout, stderr)
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
