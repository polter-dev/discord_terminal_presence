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

// daemonUpdateRefreshInterval is how often the daemon re-runs the update-check
// refresh. It is deliberately shorter than the cache lifetime: ticking at
// exactly the lifetime means a tick that lands a moment before the entry
// expires finds it still fresh, makes no lookup, and leaves the cache stale for
// nearly a whole further period. A tick whose cache entry is fresh costs one
// local file read and no network call, so the extra ticks are cheap and the
// real lookup rate stays at most one per cache lifetime.
const daemonUpdateRefreshInterval = updatepkg.CacheLifetime / 4

var releaseChecker = updatepkg.NewChecker(nil, updatepkg.DefaultCachePath())

type latestChecker interface {
	Latest(context.Context, string) (updatepkg.Result, error)
}

// automaticUpdateChecker is the daemon's view of the release checker. It uses
// Refresh rather than Check because the daemon outlives the cache: Check
// answers once per process and would leave a daemon that has been up for more
// than a cache lifetime with a stale cache and a silent alert (issue #460).
type automaticUpdateChecker interface {
	Refresh(context.Context, string, bool) (updatepkg.Result, bool)
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

// runPeriodicAutomaticUpdate keeps the shared update-check cache fresh for as
// long as the daemon runs. It performs the startup refresh, then repeats it
// every interval until ctx is cancelled.
//
// Why a loop at all: the cache entry expires after updatepkg.CacheLifetime, and
// the one-line command alert reads only that cache. A daemon started at login
// and left running — a desktop that is never rebooted, a laptop that only
// sleeps — used to refresh once and then go quiet, so the alert stopped firing
// exactly on the long-lived sessions where a release is most likely to have
// shipped (issue #460).
//
// What this promises: while the daemon runs, the cache is refreshed within
// interval of expiring. What it does not promise: an alert the instant a
// release publishes (the cache lifetime still bounds that), nor any refresh at
// all while no daemon is running.
//
// currentConfig is re-read on every tick rather than captured once, so turning
// update_check off takes effect at the next tick instead of at the next daemon
// restart, and so a config that has become unreadable stops the checks: it may
// contain an opt-out we cannot see, and privacy wins over checking. It is
// caller-supplied so it can be the daemon's live config manager.
//
// Only the refresh repeats. The install does not: see installOncePerTarget.
//
// The whole loop is best-effort: callers run it in a goroutine, so neither the
// startup refresh nor any tick can delay, block, or fail daemon startup or the
// run loop, and every failure is swallowed by runAutomaticUpdate.
func runPeriodicAutomaticUpdate(ctx context.Context, currentConfig func() (config.Config, error), current string, checker automaticUpdateChecker, runner updatepkg.CommandRunner, interval time.Duration) {
	gate := &installOncePerTarget{inner: checker, attempted: map[string]bool{}}
	refresh := func() {
		cfg, err := currentConfig()
		if err != nil {
			debugf("update refresh skipped: config unreadable: %v", err)
			return
		}
		gate.mayInstall = cfg.AutoUpdate
		runAutomaticUpdate(ctx, cfg, current, gate, runner)
	}

	refresh()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A tick that arrives together with cancellation must not start
			// another refresh; select picks randomly among ready cases.
			if ctx.Err() != nil {
				return
			}
			refresh()
		}
	}
}

// installOncePerTarget wraps the daemon's checker so a repeating refresh does
// not re-run the installer for a release it has already acted on in this
// process. The refresh half has to repeat — that is what the ticker is for —
// but the install half must not: the running process keeps reporting its own
// old version until it restarts, so without this a daemon with auto_update on
// would re-run `brew upgrade` (or re-download and re-run the generic installer)
// on every tick, whether the previous attempt succeeded or failed.
//
// Reporting "no update" for an already-attempted target suppresses only the
// install: the cache write already happened inside Refresh, and the caller's
// stale-attempt retirement still sees the version through Result.Latest.
//
// The dedupe is per process and per target, so a newly published release is
// still installed, and a failed install is retried on the next daemon start
// exactly as it was before the ticker existed.
//
// It is only ever used by runPeriodicAutomaticUpdate's single goroutine, which
// is why the fields need no locking.
type installOncePerTarget struct {
	inner      automaticUpdateChecker
	mayInstall bool
	attempted  map[string]bool
}

func (c *installOncePerTarget) Refresh(ctx context.Context, current string, configEnabled bool) (updatepkg.Result, bool) {
	result, ok := c.inner.Refresh(ctx, current, configEnabled)
	if !ok || !c.mayInstall {
		return result, ok
	}
	if c.attempted[result.Latest] {
		debugf("automatic update for %s was already attempted by this daemon; not repeating", result.Latest)
		return result, false
	}
	c.attempted[result.Latest] = true
	return result, ok
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
	// Either opt-out — update_check = false or NO_UPDATE_CHECK — makes the
	// whole automatic-update path inert: no network call for either purpose,
	// and no write to the state file below.
	//
	// The env opt-out is checked here as well as inside Checker, which is
	// redundant for the lookup and deliberate for everything after it. Relying
	// on Checker alone stopped the network call but let
	// retireStaleAutomaticUpdateAttempt run and clear a recorded attempt from
	// cached data, so the two opt-outs differed in observable side effects
	// (issue #463). Retirement is a state mutation, so "we made no network
	// call" is not the same promise as "we did nothing", and a user who set
	// NO_UPDATE_CHECK asked for the second one.
	//
	// The cost is that a stale attempt record is not retired while an opt-out
	// is in force. That is already true of update_check = false and is
	// recoverable: unset the opt-out and the next daemon start clears it.
	if !cfg.UpdateCheck || updatepkg.DisabledByEnv() || checker == nil {
		return
	}
	// Refresh writes the result to the shared cache even when the running
	// version is already current, which is what keeps the command alert
	// current for auto_update-off users.
	checkCtx, cancelCheck := context.WithTimeout(ctx, updateCheckTimeout)
	result, ok := checker.Refresh(checkCtx, current, cfg.UpdateCheck)
	cancelCheck()
	// Retiring a stale record runs on both check outcomes and ahead of the
	// auto_update branch below. A record stranded by turning auto_update off
	// after a failure is precisely the one that has to clear (issue #458), so
	// this must not sit behind that gate. result.Latest is "" unless the check
	// found something newer; retire falls back to the cache in that case.
	retireStaleAutomaticUpdateAttempt(statePath, current, result.Latest)

	if !ok {
		debugf("automatic update check skipped or found no newer release")
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
	// The success is recorded above, and `termp status` turns that record into
	// a user-visible notice for as long as this daemon keeps running the old
	// code (issue #584). This path has no user attached to write to: it runs
	// inside the daemon, whose stdout is a log file or /dev/null, so status is
	// the only place the notice can honestly reach a person.
	debugf("automatic update installed %s; it will take effect on next start", result.Latest)
}

// retireStaleAutomaticUpdateAttempt clears a recorded automatic-update failure
// that can no longer be acted on. checkedLatest is the version this run's check
// reported, or "" when the check found nothing newer or did not complete.
//
// Two independent things make a record stale:
//
//   - The running version already satisfies the target, so there is nothing
//     left to install (issue #418).
//   - The release source is not offering the target. A recorded target that is
//     not the latest release describes an attempt at something withdrawn,
//     superseded, or — as on the reporter's machine in issue #458 — never
//     published at all. The old !IsNewer rule could not reach those: a bogus
//     target such as "1.1.0" sorts newer than every shipped version, so the
//     record was immortal.
//
// The latest version comes from the check when it produced one and otherwise
// from what a previous check already wrote to the cache, because Check reports
// no result at all once the running version is current — which is exactly the
// state the reporter's machine is in. An empty latest means no check has ever
// succeeded, and no conclusion is drawn from it.
//
// Comparison is by version precedence rather than string equality: a "v"
// prefix can differ between the recorded target and a later check.
//
// A failure for the version still being offered is deliberately preserved.
// That one is real and actionable, and `termp status` is where users are told
// to look for it.
func retireStaleAutomaticUpdateAttempt(statePath, current, checkedLatest string) {
	latest := checkedLatest
	if latest == "" {
		latest = updatepkg.LastKnownLatest(statePath)
	}
	clearStaleAutomaticUpdateAttempt(statePath, func(attempt updatepkg.AutomaticUpdateAttempt) bool {
		if !updatepkg.IsNewer(current, attempt.Target) {
			return true
		}
		if attempt.Error == "" {
			// A recorded success the running version has not reached yet is
			// the "installed, not yet restarted" state that status reports
			// (issue #584). Only the rule above retires it, and it does so
			// from the restarted daemon, whose version satisfies the target.
			// The release-source rule below must not reach it: a success
			// superseded by a newer release is still an update the running
			// daemon has not picked up.
			return false
		}
		return latest != "" && !updatepkg.SameVersion(attempt.Target, latest)
	})
}

// clearStaleAutomaticUpdateAttempt drops a recorded automatic-update failure
// when stale reports it can no longer be acted on. It is best-effort by
// design: every caller is on a daemon-startup path, so a state file that
// cannot be read or written is logged and otherwise ignored rather than
// turned into an error. Whether a successful attempt (Error == "") is stale is
// decided by the caller's predicate: a success is kept while the running
// version has not reached its target, because that is the record status uses to
// tell the user the daemon still holds the old code (issue #584).
func clearStaleAutomaticUpdateAttempt(statePath string, stale func(updatepkg.AutomaticUpdateAttempt) bool) {
	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
	if !ok || !stale(attempt) {
		return
	}
	if err := updatepkg.ClearAutomaticUpdateAttempt(statePath); err != nil {
		debugf("stale automatic update attempt could not be cleared: %v", err)
		return
	}
	debugf("cleared stale automatic update attempt for %s", attempt.Target)
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
	return runUpdate(checkCtx, context.Background(), version, releaseChecker, updatepkg.ExecRunner{Interactive: true}, os.Stdin, os.Stdout, os.Stderr, updateDaemonRunning)
}

// updateDaemonRunning reports whether a daemon is running right now, using the
// same detection `termp status` uses (statusDaemonPID). Two candidates existed:
// this one and knownDaemonPID. They agree on the PID file, which is the primary
// source; they differ only in the fallback, where statusDaemonPID additionally
// requires the daemon's Discord state file to be fresh. The freshness bound is
// the right one here because the message this feeds is a claim about the state
// of the machine, and it should say exactly what the user will read back in
// `termp status` a second later. It is also the conservative direction: a stale
// state file cannot make the updater assert a daemon that status calls stopped.
func updateDaemonRunning() bool {
	return statusDaemonPID(pidFilePath(), daemonDiscordStatePath(), time.Now(), processAlive, processLooksLikeTermpAtPath) > 0
}

func runUpdate(checkCtx, updateCtx context.Context, current string, checker latestChecker, runner updatepkg.CommandRunner, stdin io.Reader, stdout, stderr io.Writer, daemonRunning func() bool) error {
	return runUpdateForPlatform(checkCtx, updateCtx, current, checker, runner, stdin, stdout, stderr, daemonRunning, runtime.GOOS)
}

// runUpdateForPlatform is runUpdate with an injectable goos so tests can
// exercise the Windows-generic-install path from any host.
func runUpdateForPlatform(checkCtx, updateCtx context.Context, current string, checker latestChecker, runner updatepkg.CommandRunner, stdin io.Reader, stdout, stderr io.Writer, daemonRunning func() bool, goos string) error {
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
			printUpdateComplete(stdout, current, result.Latest, daemonRunning)
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
	printUpdateComplete(stdout, current, result.Latest, daemonRunning)
	return nil
}

// printUpdateComplete reports a finished update. Before this, a successful run
// printed only "Updating termp from X to Y..." and returned, which reads like a
// command that was interrupted rather than one that finished (issue #584).
//
// The second half is the part that matters. On Unix, replacing the file a
// process is executing does not change that process: the running daemon keeps
// the old inode, so it goes on publishing to Discord with the old code while
// `termp version` reports the new one. The guidance is printed only when a
// daemon is actually running, so a user with no daemon up is not told to
// restart something that is not there.
//
// There is no `termp restart`, so the two real commands are named explicitly.
//
// Restarting the daemon automatically is deliberately not done here. That is a
// product decision the owner has reserved (issue #584), and an updater that
// stops and starts a daemon can orphan or double-start it.
func printUpdateComplete(stdout io.Writer, current, latest string, daemonRunning func() bool) {
	fmt.Fprintf(stdout, "Updated termp from %s to %s.\n", current, latest)
	if daemonRunning == nil || !daemonRunning() {
		return
	}
	fmt.Fprintln(stdout, "The background daemon is still running the previous version. Run \"termp stop\" then \"termp start\" to finish updating.")
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
