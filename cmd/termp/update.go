package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
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
	printAvailableUpdateContext(context.Background(), cfg, loadErr)
}

func printAvailableUpdateContext(parent context.Context, cfg config.Config, loadErr error) {
	// A malformed config may contain an opt-out we cannot safely read. Privacy
	// wins over checking in that case.
	if loadErr != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, updateCheckTimeout)
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

func printCommandUpdateAlert(command string, args []string, stderrTerminal bool, cfg config.Config, loadErr error, stderr io.Writer) {
	if loadErr != nil || cfg.AutoUpdate || !eligibleForUpdateAlert(command, args, stderrTerminal) {
		return
	}
	result, ok := releaseChecker.CachedCheck(version, cfg.UpdateCheck)
	if !ok {
		return
	}
	fmt.Fprintf(stderr, "A new version (%s) is available — run `termp update`\n", result.Latest)
}

// runAutomaticUpdate refreshes the update-check cache and, only when
// auto_update is enabled, installs the newer release.
//
// The cache refresh is deliberately not gated on auto_update: the one-line
// command alert reads that cache and never touches the network, so without a
// daemon-side refresh the users the alert exists for (auto_update off) would
// only ever see it after manually running `termp version`/`status` (issue
// #457). Installing stays opt-in — a user who turned auto_update off still
// never gets an unattended install.
//
// It is fail-open: startup callers never receive an update error. They run it
// asynchronously so even a slow package manager cannot delay the daemon. The
// installed release is used on the next daemon start.
func runAutomaticUpdate(ctx context.Context, cfg config.Config, current string, checker automaticUpdateChecker, runner updatepkg.CommandRunner) {
	runAutomaticUpdateWithStatePath(ctx, cfg, current, checker, runner, updatepkg.DefaultCachePath())
}

func runAutomaticUpdateWithStatePath(ctx context.Context, cfg config.Config, current string, checker automaticUpdateChecker, runner updatepkg.CommandRunner, statePath string) {
	runAutomaticUpdateWithStatePathForPlatform(ctx, cfg, current, checker, runner, statePath, runtime.GOOS)
}

func runAutomaticUpdateWithStatePathForPlatform(ctx context.Context, cfg config.Config, current string, checker automaticUpdateChecker, runner updatepkg.CommandRunner, statePath, goos string) {
	// update_check == false (or NO_UPDATE_CHECK) means no network call at all,
	// for either purpose. Privacy wins.
	if !cfg.UpdateCheck || checker == nil {
		return
	}
	// Check writes the result to the shared cache even when the running
	// version is already current, which is what keeps the command alert
	// current for auto_update-off users.
	checkCtx, cancelCheck := context.WithTimeout(ctx, updateCheckTimeout)
	result, ok := checker.Check(checkCtx, current, cfg.UpdateCheck)
	cancelCheck()
	if !ok {
		debugf("automatic update check skipped or found no newer release")
		// A previously recorded failure/skip can outlive its own relevance:
		// the user may have updated manually, or a later check may simply
		// no longer see a newer release. Once the recorded target is no
		// longer newer than the running version, it is stale — clear it
		// here too, rather than waiting for some future automatic attempt
		// to happen to succeed (issue #418).
		if attempt, attemptOK := updatepkg.ReadAutomaticUpdateAttempt(statePath); attemptOK && attempt.Error != "" && !updatepkg.IsNewer(current, attempt.Target) {
			if err := updatepkg.ClearAutomaticUpdateAttempt(statePath); err != nil {
				debugf("stale automatic update attempt could not be cleared: %v", err)
			} else {
				debugf("cleared stale automatic update attempt for %s", attempt.Target)
			}
		}
		return
	}

	if !cfg.AutoUpdate {
		// The cache is refreshed; installing without being asked is not on
		// the table. The command alert takes it from here.
		debugf("update check cached %s; automatic install is disabled", result.Latest)
		return
	}

	if err := automaticUpdatePlatformPreflight(goos, result.Method, result.Latest); err != nil {
		if recordErr := updatepkg.RecordAutomaticUpdateAttempt(statePath, result.Latest, time.Now(), err); recordErr != nil {
			debugf("automatic update skip could not be recorded: %v", recordErr)
		}
		debugf("automatic update skipped: %v", err)
		return
	}

	if result.Method == updatepkg.InstallGeneric {
		if err := genericAutomaticUpdatePreflight(); err != nil {
			if recordErr := updatepkg.RecordAutomaticUpdateAttempt(statePath, result.Latest, time.Now(), err); recordErr != nil {
				debugf("automatic update skip could not be recorded: %v", recordErr)
			}
			debugf("automatic update skipped: %v", err)
			return
		}
	}

	updateCtx, cancelUpdate := context.WithTimeout(ctx, automaticUpdateTimeout)
	defer cancelUpdate()
	// Homebrew owns Homebrew-installed binaries, so PerformUpdate delegates to
	// `brew upgrade polter-dev/tap/termp` instead of replacing the executable directly.
	if err := updatepkg.PerformUpdate(updateCtx, result.Method, result.Latest, runner, nil, io.Discard, io.Discard); err != nil {
		if recordErr := updatepkg.RecordAutomaticUpdateAttempt(statePath, result.Latest, time.Now(), err); recordErr != nil {
			debugf("automatic update failure could not be recorded: %v", recordErr)
		}
		debugf("automatic update skipped: %v", err)
		return
	}
	if err := updatepkg.RecordAutomaticUpdateAttempt(statePath, result.Latest, time.Now(), nil); err != nil {
		debugf("automatic update success could not be recorded: %v", err)
	}
	debugf("automatic update installed %s; it will take effect on next start", result.Latest)
}

type automaticUpdatePlatformError struct{}

func (automaticUpdatePlatformError) Error() string {
	return "generic automatic updates are not supported on Windows; run `termp update` for supported manual options"
}

func (automaticUpdatePlatformError) AutomaticUpdateSkipped() bool {
	return true
}

type automaticManagedPackageError struct {
	guidance string
}

func (e automaticManagedPackageError) Error() string {
	return fmt.Sprintf("system package installation must be updated manually:\n%s", e.guidance)
}

func (automaticManagedPackageError) AutomaticUpdateSkipped() bool {
	return true
}

func automaticUpdatePlatformPreflight(goos string, method updatepkg.InstallMethod, tag ...string) error {
	if goos == "windows" && method == updatepkg.InstallGeneric {
		return automaticUpdatePlatformError{}
	}
	if updatepkg.IsSystemPackageInstall(method) {
		guidance := "run `termp update` to see release-package installation instructions"
		if len(tag) > 0 {
			if text := updatepkg.GuidanceForMethod(method, tag[0]).Text; text != "" {
				guidance = text
			}
		}
		return automaticManagedPackageError{guidance: guidance}
	}
	return nil
}

// commandsLoadConfigForOwnAlert names commands that already call config.Load
// for their own real work and print the update alert from that same result,
// so main()'s pre-dispatch check must skip loading config a second time just
// to evaluate eligibility (issue #442).
var commandsLoadConfigForOwnAlert = map[string]bool{
	"setup":    true,
	"settings": true,
}

// eligibleForUpdateAlert reports whether a command may print the one-line
// cached-update alert. The alert should reach a human running *any* command,
// so this is a deny-list, not an allow-list (issue #457).
//
// stderrTerminal must be whether os.Stderr is a TTY, not whether the command
// itself is interactive: the alert is written to stderr, so a TTY there means
// a human is watching, while a redirected or piped stderr means a script is
// capturing output that an extra line could corrupt.
func eligibleForUpdateAlert(command string, args []string, stderrTerminal bool) bool {
	if !stderrTerminal {
		return false
	}
	if command != "help" && !knownCommand(command) {
		// A typo dispatches to the unknown-command error; do not load config
		// or nag on top of it.
		return false
	}
	switch command {
	case "update", "version", "status":
		// These run their own live check and print the richer notice; alerting
		// here would double-print. This is de-duplication, not suppression.
		return false
	case "completion":
		// Its stdout is eval'd/sourced at shell startup, and shells commonly
		// capture both streams while doing so; nagging on every new shell
		// would be actively harmful.
		return false
	default:
		return true
	}
}

func updateCommand(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: unexpected argument %q", errCommandUsage, fs.Arg(0))
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return runUpdate(checkCtx, context.Background(), version, releaseChecker, updatepkg.ExecRunner{Interactive: true}, os.Stdin, os.Stdout, os.Stderr)
}

func runUpdate(checkCtx, updateCtx context.Context, current string, checker latestChecker, runner updatepkg.CommandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	return runUpdateForPlatform(checkCtx, updateCtx, current, checker, runner, stdin, stdout, stderr, runtime.GOOS)
}

// runUpdateForPlatform is runUpdate with an injectable goos so tests can
// exercise the Windows-generic-install path from any host.
func runUpdateForPlatform(checkCtx, updateCtx context.Context, current string, checker latestChecker, runner updatepkg.CommandRunner, stdin io.Reader, stdout, stderr io.Writer, goos string) error {
	result, err := checker.Latest(checkCtx, current)
	if err != nil {
		return fmt.Errorf("unable to check for updates: %w", err)
	}
	if !updatepkg.IsNewer(current, result.Latest) {
		fmt.Fprintf(stdout, "You're already on the latest version (%s).\n", result.Latest)
		return nil
	}
	if result.Method == updatepkg.InstallScoop {
		fmt.Fprintf(stdout, "Update available: %s -> %s\n\n", current, result.Latest)
		fmt.Fprintln(stdout, "To update:")
		fmt.Fprintf(stdout, "  %s\n", updatepkg.GuidanceForMethod(result.Method, result.Latest).Text)
		return nil
	}
	if goos == "windows" && result.Method == updatepkg.InstallGeneric {
		// Windows locks a running executable, so a generic (archive/zip)
		// install can never self-update — genericUpdatePlatformError refuses
		// it every time. That's a permanent platform limitation, not a
		// transient failure, so there is nothing to "retry": skip the
		// attempt entirely and hand the user the two real options up front.
		fmt.Fprintf(stdout, "Update available: %s -> %s\n\n", current, result.Latest)
		fmt.Fprintln(stdout, "termp cannot update a Windows archive install.")
		fmt.Fprintln(stdout, "To update:")
		fmt.Fprintln(stdout, updatepkg.WindowsArchiveGuidance(result.Latest).Text)
		return nil
	}
	if updatepkg.IsSystemPackageInstall(result.Method) {
		fmt.Fprintln(stdout, "termp is managed by your system package manager.")
		fmt.Fprintf(stdout, "Updating termp from %s to %s...\n", current, result.Latest)
		if err := updatepkg.PerformUpdate(updateCtx, result.Method, result.Latest, runner, stdin, stdout, stderr); err == nil {
			return nil
		} else {
			fmt.Fprintf(stderr, "termp update: automatic package update unavailable: %v\n", err)
			fmt.Fprintln(stdout, "To update:")
			fmt.Fprintln(stdout, updatepkg.GuidanceForMethod(result.Method, result.Latest).Text)
			return nil
		}
	}
	retryCommand, err := updatepkg.UpdateCommandForMethod(result.Method, result.Latest)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Updating termp from %s to %s...\n", current, result.Latest)
	// Generic installs use the exact-tag installer before replacing the binary.
	if err := updatepkg.PerformUpdate(updateCtx, result.Method, result.Latest, runner, stdin, stdout, stderr); err != nil {
		if result.Method == updatepkg.InstallGeneric {
			fmt.Fprintln(stderr, "termp update: update failed; resolve the error above, then retry: termp update")
		} else {
			fmt.Fprintf(stderr, "termp update: retry with: %s\n", updateRetryCommand(retryCommand))
		}
		return err
	}
	return nil
}

func updateRetryCommand(command updatepkg.Command) string {
	if command.Name == "sh" && len(command.Args) == 2 && command.Args[0] == "-c" {
		return command.Args[1]
	}
	return strings.Join(append([]string{command.Name}, command.Args...), " ")
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
	header := fmt.Sprintf("Update available: %s -> %s", result.Current, result.Latest)

	lines := wrapOutputText(header, width)
	for i := range lines {
		lines[i] = style(lines[i], headerStyle)
	}
	guidance := result.Guidance
	if result.Method == updatepkg.InstallGeneric {
		guidance = updatepkg.Guidance{Text: "termp update", Runnable: true}
	}
	label := "To update:"
	if guidance.Runnable {
		label = "Run:"
	}
	lines = append(lines, "", label)
	commandLines := wrapShellCommand(guidance.Text, width-2)
	if !guidance.Runnable {
		commandLines = wrapUpdateGuidance(guidance.Text, width-2)
	}
	for _, line := range commandLines {
		if line == "" {
			lines = append(lines, "")
		} else {
			lines = append(lines, "  "+style(line, commandStyle))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func wrapUpdateGuidance(guidance string, width int) []string {
	var lines []string
	for _, line := range strings.Split(guidance, "\n") {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapShellCommand(line, width)...)
	}
	return lines
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
