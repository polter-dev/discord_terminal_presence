package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xterm "github.com/charmbracelet/x/term"
	"github.com/polter-dev/discord_terminal_presence/internal/completioninstall"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
	"github.com/polter-dev/discord_terminal_presence/internal/terminaltext"
	"github.com/polter-dev/discord_terminal_presence/internal/tui"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
	usagepkg "github.com/polter-dev/discord_terminal_presence/internal/usage"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	verbose bool
)

const (
	stopTimeout                     = 5 * time.Second
	stopPollInterval                = 50 * time.Millisecond
	statusTimeout                   = 2500 * time.Millisecond
	daemonDiscordStateWriteInterval = 15 * time.Second
	daemonDiscordStateStaleAfter    = 45 * time.Second
)

var errDaemonNotRunning = errors.New("daemon is not running")

// errPIDRecordUnparseable marks a PID file whose contents could not be
// decoded (garbage bytes, truncated JSON, etc.) as distinct from an I/O
// failure or an ordinary missing file. Stop paths treat it like a stale PID
// file: remove it and report the daemon as not running, rather than
// surfacing the raw parser error (issue #491).
var errPIDRecordUnparseable = errors.New("PID record is unparseable")

// errUnreadablePIDFileRemoved is returned by the stop paths after an
// unparseable PID file has been removed on a best-effort basis.
var errUnreadablePIDFileRemoved = errors.New("removed an unreadable PID file; daemon is not running")

var commandHelp = []struct {
	name        string
	description string
}{
	{"autostart", "manage login autostart with grouped actions"},
	{"install", "alias for \"termp autostart install\" (not the binary)"},
	{"uninstall", "remove start-at-login; --all removes all termp-created data"},
	{"disable", "alias for \"termp autostart disable\""},
	{"enable", "alias for \"termp autostart enable\""},
	{"start", "run daemon (background by default; --foreground keeps it attached)"},
	{"stop", "stop the running daemon (autostart controls login startup)"},
	{"connect", "re-establish the daemon's Discord connection"},
	{"status", "show daemon, Discord, autostart, and config status"},
	{"settings", "open the interactive settings TUI"},
	{"watch", "preview the live Discord card (--once prints one snapshot)"},
	{"version", "print version and build information"},
	{"update", "update termp using its detected install method"},
	{"setup", "run the interactive first-run configuration wizard"},
	{"config", "manage configuration non-interactively"},
	{"completion", "generate a shell completion script"},
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("termp: ")

	command, args, showVersion, err := parseRoot(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) && len(os.Args) == 1 {
			if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
				// Bare `termp` runs the watch TUI without going through
				// dispatchCommand, so it used to return below before ever
				// reaching the pre-dispatch alert — the one invocation most
				// likely to be a human's was the only one that never told them
				// about a new version (issue #457).
				maybePrintCommandUpdateAlert("watch", nil, isTerminal(os.Stderr), os.Stderr)
				if err := watch(nil); err != nil {
					log.Fatal(err)
				}
				return
			}
			maybePrintFirstRunCTA(os.Stdout, config.DefaultPath(), isTerminal(os.Stdout))
		}
		if errors.Is(err, flag.ErrHelp) && rootHelpRequested(os.Args[1:]) {
			usage(os.Stdout)
			return
		}
		usage(os.Stderr)
		os.Exit(2)
	}
	if showVersion {
		printCompactVersion()
		return
	}

	maybePrintCommandUpdateAlert(command, args, isTerminal(os.Stderr), os.Stderr)

	err = dispatchCommand(command, args)
	if printDispatchUsageError(err, os.Stderr) {
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

var errUnknownCommand = errors.New("unknown command")

func printDispatchUsageError(err error, w io.Writer) bool {
	if errors.Is(err, flag.ErrHelp) {
		return true
	}
	if errors.Is(err, errCommandUsage) {
		fmt.Fprintln(w, err)
		return true
	}
	if !errors.Is(err, errUnknownCommand) {
		return false
	}
	fmt.Fprintln(w, err)
	if suggestion := closestCommand(unknownCommandName(err), commandNames(), 2); suggestion != "" {
		fmt.Fprintf(w, "Did you mean %q?\n", suggestion)
		return true
	}
	usage(w)
	return true
}

func dispatchCommand(command string, args []string) error {
	return dispatchCommandWithAutostartHandlers(command, args, autostartActionHandlers())
}

func dispatchCommandWithAutostartHandlers(command string, args []string, handlers map[string]autostartActionHandler) error {
	if command != "help" && !knownCommand(command) {
		return fmt.Errorf("%w %q", errUnknownCommand, command)
	}
	var err error
	switch command {
	case "install", "disable", "enable", "status":
		err = dispatchAutostartAction(command, args, handlers)
	case "uninstall":
		err = dispatchAutostartAction(command, args, handlers)
		if err == nil && !uninstallAllRequested(args) {
			fmt.Println("To remove everything, run: termp uninstall --all")
		}
	case "autostart":
		err = dispatchAutostartCommand(args, handlers)
	case "start":
		err = start(args)
	case "stop":
		err = stop(args)
	case "connect":
		err = connectCommand(args)
	case "settings":
		err = settings(args)
	case "watch":
		err = watch(args)
	case "version":
		err = versionCommand(args)
	case "update":
		err = updateCommand(args)
	case "setup":
		err = setup(args)
	case "config":
		err = configCommand(args)
	case "completion":
		err = completion(args)
	case "help":
		usage(os.Stdout)
	}
	if errors.Is(err, flag.ErrHelp) && rootHelpRequested(args) {
		return nil
	}
	return err
}

func uninstallAllRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--all" || arg == "--all=true" {
			return true
		}
	}
	return false
}

func commandNames() []string {
	names := make([]string, 0, len(commandHelp))
	for _, command := range commandHelp {
		if command.name == "connect" && !connectSupported {
			continue
		}
		names = append(names, command.name)
	}
	return names
}

func knownCommand(name string) bool {
	for _, command := range commandHelp {
		if command.name == name {
			return true
		}
	}
	return false
}

func unknownCommandName(err error) string {
	message := strings.TrimPrefix(err.Error(), errUnknownCommand.Error()+" ")
	name, unquoteErr := strconv.Unquote(message)
	if unquoteErr != nil {
		return ""
	}
	return name
}

func closestCommand(input string, commands []string, maxDistance int) string {
	best := ""
	bestDistance := maxDistance + 1
	for _, command := range commands {
		distance := levenshteinDistance(input, command)
		if distance < bestDistance {
			best = command
			bestDistance = distance
		}
	}
	if bestDistance > maxDistance {
		return ""
	}
	return best
}

func levenshteinDistance(a, b string) int {
	aRunes := []rune(a)
	bRunes := []rune(b)
	previous := make([]int, len(bRunes)+1)
	for i := range previous {
		previous[i] = i
	}
	for i, aRune := range aRunes {
		current := make([]int, len(previous))
		current[0] = i + 1
		for j, bRune := range bRunes {
			cost := 0
			if aRune != bRune {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(previous)-1]
}

func rejectUnexpectedArgs(fs *flag.FlagSet, usageLine string) error {
	if fs.NArg() == 0 {
		return nil
	}
	fmt.Fprintf(fs.Output(), "usage: %s\n", usageLine)
	return fmt.Errorf("%w: unexpected argument %q", errCommandUsage, fs.Arg(0))
}

func parseCommandFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errCommandUsage, err)
	}
	return nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Terminal Presence (termp)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  termp [--verbose] [--version] <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, command := range commandHelp {
		if command.name == "connect" && !connectSupported {
			continue
		}
		fmt.Fprintf(w, "  %-10s  %s\n", command.name, command.description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global options:")
	fmt.Fprintln(w, "  -v, --verbose  enable verbose logging")
	fmt.Fprintln(w, "  --version      print version information")
	fmt.Fprintln(w, "  -h, --help     show this help")
}

func rootHelpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func parseRoot(args []string) (command string, commandArgs []string, showVersion bool, err error) {
	fs := flag.NewFlagSet("termp", flag.ContinueOnError)
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	fs.BoolVar(&verbose, "v", false, "enable verbose logging")
	fs.BoolVar(&showVersion, "version", false, "print version information")
	fs.Usage = func() {}
	if err := parseCommandFlags(fs, args); err != nil {
		return "", nil, false, err
	}
	if showVersion {
		return "", nil, true, nil
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		return "", nil, false, flag.ErrHelp
	}
	return remaining[0], remaining[1:], false, nil
}

func addVerboseFlag(fs *flag.FlagSet) {
	fs.BoolVar(&verbose, "verbose", verbose, "enable verbose logging")
	fs.BoolVar(&verbose, "v", verbose, "enable verbose logging")
}

func debugf(format string, args ...any) {
	if verbose {
		log.Print(terminaltext.SanitizeSingleLine(fmt.Sprintf(format, args...)))
	}
}

func versionCommand(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp version [--verbose]"); err != nil {
		return err
	}
	fmt.Print(formatVersion(currentVersionInfo()))
	cfg, loadErr := config.LoadReadOnly()
	printAvailableUpdate(cfg, loadErr)
	return nil
}

type versionInfo struct {
	version   string
	commit    string
	built     string
	dateLabel string
	goVersion string
	platform  string
}

func currentVersionInfo() versionInfo {
	return resolveVersionInfo(version, commit, date, debug.ReadBuildInfo)
}

type buildInfoReader func() (*debug.BuildInfo, bool)

func resolveVersionInfo(stampedVersion, stampedCommit, stampedDate string, readBuildInfo buildInfoReader) versionInfo {
	info := versionInfo{
		version:   stampedVersion,
		commit:    stampedCommit,
		built:     stampedDate,
		dateLabel: "Built",
		goVersion: runtime.Version(),
		platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if stampedVersion != "dev" && stampedCommit != "none" && stampedDate != "unknown" {
		return info
	}

	buildInfo, ok := readBuildInfo()
	if !ok || buildInfo == nil {
		return info
	}
	if info.version == "dev" && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)" {
		info.version = buildInfo.Main.Version
	}
	for _, setting := range buildInfo.Settings {
		switch {
		case info.commit == "none" && setting.Key == "vcs.revision" && setting.Value != "":
			info.commit = shortRevision(setting.Value)
		case info.built == "unknown" && setting.Key == "vcs.time" && setting.Value != "":
			info.built = setting.Value
			info.dateLabel = "Commit time"
		}
	}
	return info
}

func shortRevision(revision string) string {
	const shortLength = 7
	if len(revision) <= shortLength {
		return revision
	}
	return revision[:shortLength]
}

func normalizedDateLabel(info versionInfo) string {
	if info.dateLabel == "" {
		return "Built"
	}
	return info.dateLabel
}

func printCompactVersion() {
	fmt.Print(formatCompactVersion(currentVersionInfo()))
}

func formatCompactVersion(info versionInfo) string {
	return fmt.Sprintf("termp %s (%s, %s)\ngo %s\n%s\n",
		info.version, info.commit, info.built, info.goVersion, info.platform)
}

func formatVersion(info versionInfo) string {
	return formatSections("termp", outputSection{fields: []outputField{
		{label: "Version", value: info.version},
		{label: "Commit", value: info.commit},
		{label: normalizedDateLabel(info), value: info.built},
		{label: "Go", value: info.goVersion},
		{label: "Platform", value: info.platform},
	}})
}

func configCommand(args []string) error {
	if len(args) == 0 {
		configUsage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "-h", "--help":
		configUsage()
		return flag.ErrHelp
	case "init":
		return configInit(args[1:])
	default:
		configUsage()
		return fmt.Errorf("%w: unknown config action %q", errCommandUsage, args[0])
	}
}

func configUsage() {
	fmt.Fprintln(os.Stderr, "usage: termp config init [--force]")
}

func configInit(args []string) error {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	addVerboseFlag(fs)
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp config init [--force]"); err != nil {
		return err
	}
	path := config.DefaultPath()
	if err := config.InitFile(path, *force); err != nil {
		return err
	}
	if *force {
		fmt.Printf("Reset config to defaults: %s\n", path)
		fmt.Println("Run \"termp setup\" to configure interactively.")
		return nil
	}
	fmt.Printf("Wrote default config: %s\n", path)
	return nil
}

func setup(args []string) error {
	return setupWithConfigLoader(args, config.Load)
}

func setupWithConfigLoader(args []string, load func() (config.Config, error)) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp setup [--verbose]"); err != nil {
		return err
	}
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	cfg, err := loadConfigWithNotice(load, os.Stderr)
	if err != nil {
		return err
	}
	printCommandUpdateAlert("setup", args, isTerminal(os.Stderr), cfg, nil, os.Stderr)
	save := func(cfg config.Config) (string, error) {
		path := config.DefaultPath()
		return path, config.Save(cfg, path)
	}
	if !interactive {
		path, err := save(cfg)
		if err != nil {
			return err
		}
		fmt.Printf("Wrote default config: %s\n", path)
		fmt.Println("Non-interactive setup skipped autostart and shell completion. Run `termp setup` in a terminal to opt in, or `termp autostart install` to enable autostart.")
		return nil
	}
	model := newSetupModel(cfg, save, service.NewManager(), service.ResolveExecutable)
	finalModel, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	if setupModel, ok := finalModel.(tui.SetupModel); ok && setupModel.Err() != nil {
		return setupModel.Err()
	}
	return nil
}

type setupServiceManager interface {
	Install(string, bool) (service.State, error)
	InstallDefinition(string, bool) (service.State, error)
	Uninstall(bool) (service.State, error)
	Status() service.State
}

func newSetupModel(cfg config.Config, save tui.SetupSaveFunc, manager setupServiceManager, exe tui.SetupExeFunc) tui.SetupModel {
	installAutostart := func(path string) error {
		return installSetupAutostart(manager, path, currentDaemonPID() > 0)
	}
	uninstallAutostart := func() error {
		_, err := manager.Uninstall(false)
		return err
	}
	autostartInstalled := func() (bool, error) {
		return manager.Status().Installed, nil
	}
	model := tui.NewSetupModel(cfg, save, installAutostart, uninstallAutostart, exe, autostartInstalled)
	shell := completioninstall.DetectShell(os.Getenv("SHELL"))
	path, err := completioninstall.TargetPath(shell, os.UserHomeDir)
	if err != nil {
		return model
	}
	script, err := completionScript(shell)
	if err != nil {
		return model
	}
	return model.WithCompletion(shell, path, completioninstall.Note(shell), func() ([]string, error) {
		return completioninstall.Install(shell, script, os.UserHomeDir)
	})
}

func installSetupAutostart(manager setupServiceManager, path string, daemonRunning bool) error {
	install := manager.Install
	if daemonRunning {
		install = manager.InstallDefinition
	}
	_, err := install(path, false)
	return err
}

func currentDaemonPID() int {
	return knownDaemonPID(pidFilePath(), daemonDiscordStatePath(), processAlive, processLooksLikeTermpAtPath)
}

func completion(args []string) error {
	if len(args) > 0 && args[0] == "uninstall" {
		return completionUninstall(args[1:])
	}

	fs := flag.NewFlagSet("completion", flag.ContinueOnError)
	addVerboseFlag(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: termp completion <bash|zsh|fish>")
		fmt.Fprintln(os.Stderr, "       termp completion uninstall [bash|zsh|fish]")
		fmt.Fprintln(os.Stderr, "bash: source <(termp completion bash)")
		fmt.Fprintln(os.Stderr, "zsh:  termp completion zsh > ${fpath[1]}/_termp")
		fmt.Fprintln(os.Stderr, "fish: termp completion fish > ~/.config/fish/completions/termp.fish")
	}
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	script, err := completionScript(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("%w: %v", errCommandUsage, err)
	}
	fmt.Print(script)
	return nil
}

func completionUninstall(args []string) error {
	fs := flag.NewFlagSet("completion uninstall", flag.ContinueOnError)
	addVerboseFlag(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: termp completion uninstall [bash|zsh|fish]")
	}
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	if fs.NArg() == 1 && !supportedCompletionShell(fs.Arg(0)) {
		return fmt.Errorf("%w: unsupported shell %q (expected bash, zsh, or fish)", errCommandUsage, fs.Arg(0))
	}

	var (
		paths []string
		err   error
	)
	if fs.NArg() == 0 {
		paths, err = completioninstall.UninstallAll(os.UserHomeDir)
	} else {
		paths, err = completioninstall.Uninstall(fs.Arg(0), os.UserHomeDir)
	}
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fmt.Println("No shell completion was installed.")
		return nil
	}
	for _, path := range paths {
		fmt.Printf("Removed shell completion: %s\n", path)
	}
	return nil
}

func supportedCompletionShell(shell string) bool {
	return shell == "bash" || shell == "zsh" || shell == "fish"
}

func completionScript(shell string) (string, error) {
	commands := strings.Join(commandNames(), " ")
	connectCase := ""
	if connectSupported {
		connectCase = `    connect)
      COMPREPLY=( $(compgen -W "--force --verbose -v --help -h" -- "$cur") )
      ;;
`
	}
	switch shell {
	case "bash":
		return `# termp bash completion.
# Enable in the current session: source <(termp completion bash)
# Or install permanently: termp completion bash > ~/.local/share/bash-completion/completions/termp
_termp_complete() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local command=""
  local i

  for ((i = 1; i < COMP_CWORD; i++)); do
    case "${COMP_WORDS[i]}" in
      -*) ;;
      *) command="${COMP_WORDS[i]}"; break ;;
    esac
  done

  if [[ -z "$command" ]]; then
    COMPREPLY=( $(compgen -W "--verbose -v --version --help -h ` + commands + `" -- "$cur") )
    return
  fi

  case "$command" in
    config)
      if [[ " ${COMP_WORDS[*]} " == *" init "* ]]; then
        COMPREPLY=( $(compgen -W "--force --verbose -v --help -h" -- "$cur") )
      else
        COMPREPLY=( $(compgen -W "init --help -h" -- "$cur") )
      fi
      ;;
    completion)
      COMPREPLY=( $(compgen -W "bash zsh fish uninstall --verbose -v --help -h" -- "$cur") )
      ;;
    autostart)
      if [[ " ${COMP_WORDS[*]} " == *" install "* ]]; then
        COMPREPLY=( $(compgen -W "--force --help -h" -- "$cur") )
      elif [[ " ${COMP_WORDS[*]} " == *" status "* ]]; then
        COMPREPLY=( $(compgen -W "--verbose -v --help -h" -- "$cur") )
      else
        COMPREPLY=( $(compgen -W "enable disable status install uninstall --help -h" -- "$cur") )
      fi
      ;;
    install)
      COMPREPLY=( $(compgen -W "--force --help -h" -- "$cur") )
      ;;
    uninstall)
      COMPREPLY=( $(compgen -W "--all --yes --force --help -h" -- "$cur") )
      ;;
    watch)
      COMPREPLY=( $(compgen -W "--once --verbose -v --help -h" -- "$cur") )
      ;;
    start)
      COMPREPLY=( $(compgen -W "--foreground -f --detach -d --verbose -v --help -h" -- "$cur") )
      ;;
` + connectCase + `    stop|status|settings|version|update|setup)
      COMPREPLY=( $(compgen -W "--verbose -v --help -h" -- "$cur") )
      ;;
    *)
      COMPREPLY=( $(compgen -W "--help -h" -- "$cur") )
      ;;
  esac
}
complete -F _termp_complete termp
`, nil
	case "zsh":
		connectCase = ""
		if connectSupported {
			connectCase = `      connect)
        compadd -- --force --verbose -v --help -h
        ;;
`
		}
		return `#compdef termp
# termp zsh completion.
# Enable in the current session: source <(termp completion zsh)
# Or install permanently: termp completion zsh > ${fpath[1]}/_termp
_termp() {
  local command word
  for word in $words[2,-1]; do
    case $word in
      ` + strings.ReplaceAll(commands, " ", "|") + `) command=$word; break ;;
    esac
  done

  if [[ -n $command ]]; then
    case $command in
      config)
        if [[ " ${words[*]} " == *" init "* ]]; then
          compadd -- --force --verbose -v --help -h
        else
          compadd -- init --help -h
        fi
        ;;
      completion)
        compadd -- bash zsh fish uninstall --verbose -v --help -h
        ;;
      autostart)
        if [[ " ${words[*]} " == *" install "* ]]; then
          compadd -- --force --help -h
        elif [[ " ${words[*]} " == *" status "* ]]; then
          compadd -- --verbose -v --help -h
        else
          compadd -- enable disable status install uninstall --help -h
        fi
        ;;
      install)
        compadd -- --force --help -h
        ;;
      uninstall)
        compadd -- --all --yes --force --help -h
        ;;
      watch)
        compadd -- --once --verbose -v --help -h
        ;;
      start)
        compadd -- --foreground -f --detach -d --verbose -v --help -h
        ;;
` + connectCase + `      stop|status|settings|version|update|setup)
        compadd -- --verbose -v --help -h
        ;;
      *)
        compadd -- --help -h
        ;;
    esac
    return
  fi

  _arguments \
    '(-v --verbose)'{-v,--verbose}'[enable verbose logging]' \
    '--version[print version information]' \
    '(-h --help)'{-h,--help}'[show help]' \
    '1:command:(` + commands + `)'
}
compdef _termp termp
`, nil
	case "fish":
		var b strings.Builder
		b.WriteString("# termp fish completion.\n")
		b.WriteString("# Enable in the current session: termp completion fish | source\n")
		b.WriteString("# Or install permanently: termp completion fish > ~/.config/fish/completions/termp.fish\n")
		b.WriteString("complete -c termp -f\n")
		commandCondition := "not __fish_seen_subcommand_from " + commands
		b.WriteString(fmt.Sprintf("complete -c termp -n '%s' -s v -l verbose -d 'enable verbose logging'\n", commandCondition))
		b.WriteString(fmt.Sprintf("complete -c termp -n '%s' -l version -d 'print version information'\n", commandCondition))
		b.WriteString(fmt.Sprintf("complete -c termp -n '%s' -s h -l help -d 'show help'\n", commandCondition))
		for _, command := range strings.Fields(commands) {
			b.WriteString(fmt.Sprintf("complete -c termp -n '%s' -a %s\n", commandCondition, command))
		}
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from config; and not __fish_seen_subcommand_from init' -a init\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from config; and __fish_seen_subcommand_from init' -l force -d 'overwrite an existing config'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish uninstall'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from autostart; and not __fish_seen_subcommand_from enable disable status install uninstall' -a 'enable disable status install uninstall'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from autostart; and __fish_seen_subcommand_from install' -l force -d 'install even when the executable path is unstable'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from autostart; and __fish_seen_subcommand_from status' -s v -l verbose -d 'enable verbose logging'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from install' -l force -d 'install even when the executable path is unstable'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from uninstall' -l all -d 'remove all termp-created data'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from uninstall' -l yes -d 'skip full-uninstall confirmation'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from uninstall' -l force -d 'remove another installation task'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from watch' -l once -d 'render one preview snapshot and exit'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from start' -s f -l foreground -d 'keep the daemon attached to the terminal'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from start' -s d -l detach -d 'start the daemon in the background (default)'\n")
		if connectSupported {
			b.WriteString("complete -c termp -n '__fish_seen_subcommand_from connect' -l force -d 'reconnect even when already connected'\n")
		}
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from " + commands + "' -s v -l verbose -d 'enable verbose logging'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from config; and __fish_seen_subcommand_from init' -s v -l verbose -d 'enable verbose logging'\n")
		b.WriteString("complete -c termp -n '__fish_seen_subcommand_from " + commands + "' -s h -l help -d 'show help'\n")
		return b.String(), nil
	default:
		return "", fmt.Errorf("unsupported shell %q (want bash, zsh, or fish)", shell)
	}
}

func start(args []string) error {
	options, err := parseStartOptions(args, verbose, os.Stderr)
	if err != nil {
		return err
	}
	verbose = options.verbose
	ownsDaemonLog := options.detachedChild || options.daemonLog
	if ownsDaemonLog {
		logPath, err := detachedLogPath()
		if err != nil {
			return err
		}
		logWriter, err := newRotatingLogWriter(logPath, detachedLogMaxBytes, detachedLogRetained)
		if err != nil {
			return err
		}
		if err := logWriter.RedirectStderr(); err != nil {
			_ = logWriter.Close()
			return err
		}
		log.SetOutput(logWriter)
	}
	if !ownsDaemonLog {
		maybePrintFirstRunCTA(os.Stdout, config.DefaultPath(), isTerminal(os.Stdout))
	}

	pidPath := pidFilePath()
	if record := knownDaemonRecord(pidPath, daemonDiscordStatePath(), processAlive, processLooksLikeTermpAtPath); record.PID > 0 {
		currentPath, pathErr := currentProcessExecutablePath()
		if pathErr != nil {
			return fmt.Errorf("resolve current executable: %w", pathErr)
		}
		return daemonAlreadyRunningError(record, currentPath)
	}
	background := !options.foreground
	if background && !options.detachedChild {
		cfg, loadErr := config.LoadReadOnly()
		if loadErr != nil {
			printStartupConfigError(os.Stderr, cfg.Path, loadErr)
		}
		pid, logPath, err := spawnDetachedStart(options.verbose)
		if err != nil {
			return err
		}
		fmt.Printf("termp started in the background (pid %d); stop with 'termp stop'; logs: %s\n", pid, logPath)
		return nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	control := newDaemonControl()
	stopControlServer, err := startControlServer(ctx, os.Getpid(), control.handle)
	if err != nil {
		return err
	}
	defer stopControlServer()

	stopShutdownWatch, err := installShutdownSignal(cancel)
	if err != nil {
		return err
	}
	defer stopShutdownWatch()

	pidInfo, err := writePIDOwned(pidPath, os.Getpid())
	if err != nil {
		return err
	}
	defer func() {
		_, _ = removePIDIfOwned(pidPath, os.Getpid(), pidInfo)
	}()

	configPath := config.DefaultPath()
	manager, cfg, loadErr := newWatchedConfigManager(configPath, func(manager *config.Manager) {
		startConfigWatchWithRetry(ctx, manager, configPath)
	})
	if loadErr != nil {
		log.Print(startupConfigError(cfg.Path, loadErr))
	}
	logConfigWarnings(cfg.Warnings)

	// Refreshing the update-check cache (and, if auto_update is on, installing)
	// is best-effort and asynchronous: it is triggered before the run loop, but
	// can never delay or prevent daemon startup. It then repeats on a ticker so
	// the cache does not go stale on a daemon that stays up past the cache
	// lifetime (issue #460), and stops with ctx on shutdown. The config is read
	// per refresh through the manager, so an opt-out applies from the next tick.
	go runPeriodicAutomaticUpdate(ctx, manager.Current, version, releaseChecker, updatepkg.ExecRunner{Interactive: false}, daemonUpdateRefreshInterval)

	return run(ctx, manager, control)
}

// newWatchedConfigManager centralizes the safe startup ordering shared by the
// daemon and interactive watch entry points: install the watch before the
// settled load so a save completion during startup is either observed by the
// load or queued as a watch event. Watch never loads an already-settled file
// on its own, so the explicit Reload is required even after a successful
// watch installation.
func newWatchedConfigManager(path string, installWatch func(*config.Manager)) (*config.Manager, config.Config, error) {
	manager := config.NewManagerPath(path)
	installWatch(manager)
	_ = manager.Reload()
	// Reload publishes for already-running consumers. Startup has none yet,
	// and it handles the Current result below through the existing startup
	// warning/error path, so do not replay the same load as a hot reload.
	select {
	case <-manager.Reloads():
	default:
	}
	cfg, err := manager.Current()
	return manager, cfg, err
}

// configWatchRetryInterval bounds how long a user who fixes the config
// directory/file after a failed watch start (issue #416) waits for the
// daemon to notice on its own, without a restart.
const configWatchRetryInterval = 30 * time.Second

// startConfigWatchWithRetry starts the config file watcher, logging exactly
// like `termp watch` does (config.EnsureConfigDir failing here previously
// left no log line at all — issue #416) when it cannot start immediately. A
// watch that fails to start is not a permanent condition: the directory can
// be a stray file that gets removed, or a permission problem that gets
// fixed, so this keeps retrying in the background instead of leaving the
// daemon's "presence is off until the config is valid" startup message
// permanently unfulfillable.
func startConfigWatchWithRetry(ctx context.Context, manager *config.Manager, path string) {
	if tryStartConfigWatch(ctx, manager, path, log.Printf) {
		return
	}
	go retryConfigWatch(ctx, manager, path, configWatchRetryInterval)
}

// tryStartConfigWatch attempts to start the watcher once, logging any
// failure through logf (retryConfigWatch passes nil to avoid repeating the
// same failure every interval). The watcher goroutine started by a
// successful manager.Watch is bound to ctx, so it still stops on shutdown.
func tryStartConfigWatch(ctx context.Context, manager *config.Manager, path string, logf func(string, ...any)) bool {
	if err := config.EnsureConfigDir(path); err != nil {
		if logf != nil {
			logf("config watch disabled: %v", err)
		}
		return false
	}
	if err := manager.Watch(ctx); err != nil {
		if logf != nil {
			logf("config watch disabled: %v", err)
		}
		return false
	}
	return true
}

// retryConfigWatch periodically retries starting the watch until it
// succeeds or ctx is cancelled. interval is a parameter (rather than always
// reading the configWatchRetryInterval constant) so tests can exercise
// self-healing without a 30-second wait.
func retryConfigWatch(ctx context.Context, manager *config.Manager, path string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Quiet on repeated failure so a permanently broken config
			// directory does not spam the log every interval; the first
			// failure was already logged by startConfigWatchWithRetry.
			if tryStartConfigWatch(ctx, manager, path, nil) {
				log.Print("config watch recovered")
				// manager.Watch only reacts to fsnotify events from here
				// on; it never loads the file that is already sitting on
				// disk. In the #416 repro the user fixes the directory and
				// drops in a valid config in one burst, before the watch
				// exists to see any event for it, so without this the
				// daemon would stay on its stale startup error until a
				// second, unrelated edit happened to arrive.
				_ = manager.Reload()
				return
			}
		}
	}
}

func startupConfigError(path string, err error) string {
	return terminaltext.SanitizeSingleLine(fmt.Sprintf(
		"config load failed for %s; presence is off until the config is valid: %v",
		path,
		err,
	))
}

func printStartupConfigError(w io.Writer, path string, err error) {
	fmt.Fprintf(w, "termp: %s\n", startupConfigError(path, err))
}

func logConfigWarnings(warnings []string) {
	for _, warning := range warnings {
		log.Print(terminaltext.SanitizeSingleLine(warning))
	}
}

func configReloadFailure(err error) string {
	return terminaltext.SanitizeSingleLine(fmt.Sprintf(
		"config reload failed; keeping last-good behavior: %v",
		err,
	))
}

func logConfigReloadFailure(err error) {
	log.Print(configReloadFailure(err))
}

func configWatchFailure(err error) string {
	return terminaltext.SanitizeSingleLine(fmt.Sprintf(
		"config watcher error; continuing with current config: %v",
		err,
	))
}

func logConfigWatchFailure(err error) {
	log.Print(configWatchFailure(err))
}

type startOptions struct {
	detach        bool
	detachedChild bool
	daemonLog     bool
	foreground    bool
	verbose       bool
}

func parseStartOptions(args []string, defaultVerbose bool, output io.Writer) (startOptions, error) {
	options := startOptions{verbose: defaultVerbose}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.BoolVar(&options.detach, "detach", false, "start the daemon in the background")
	fs.BoolVar(&options.detach, "d", false, "start the daemon in the background")
	fs.BoolVar(&options.detachedChild, detachedChildFlag, false, "internal detached child marker")
	fs.BoolVar(&options.daemonLog, daemonLogFlag, false, "internal daemon log marker")
	fs.BoolVar(&options.foreground, "foreground", false, "keep the daemon attached to the terminal")
	fs.BoolVar(&options.foreground, "f", false, "keep the daemon attached to the terminal")
	fs.BoolVar(&options.verbose, "verbose", defaultVerbose, "enable verbose logging")
	fs.BoolVar(&options.verbose, "v", defaultVerbose, "enable verbose logging")
	fs.Usage = func() {
		fmt.Fprintln(output, "usage: termp start [--foreground]")
		fmt.Fprintln(output, "  -f, --foreground keep the daemon attached to the terminal")
		fmt.Fprintln(output, "  -d, --detach     start the daemon in the background (default)")
		fmt.Fprintln(output, "  -v, --verbose    enable verbose logging")
		fmt.Fprintln(output)
		fmt.Fprintln(output, "start/stop control the current daemon lifetime.")
		fmt.Fprintln(output, "Use \"termp autostart install\" to start automatically at login.")
	}
	if err := parseCommandFlags(fs, args); err != nil {
		return startOptions{}, err
	}
	if err := rejectUnexpectedArgs(fs, "termp start [--foreground] [--detach] [--verbose]"); err != nil {
		return startOptions{}, err
	}
	return options, nil
}

func run(ctx context.Context, manager *config.Manager, control *daemonControl) error {
	cfg, initialConfigErr := manager.Current()
	fallbackMessage := selectFallbackMessage(cfg.FallbackMessages)
	applied, err := newDetectionRuntime(cfg)
	if err != nil {
		return err
	}
	det, err := detector.New(applied.registry, detector.NewGopsutilLister(), applied.detectorConfig)
	if err != nil {
		return err
	}
	if verbose {
		det.SetDebugf(debugf)
	}
	debugf("daemon started: scan_interval=%s", applied.detectorConfig.ScanInterval)

	writerOptions := []presence.WriterOption{}
	statePublishers := runDaemonDiscordStatePublisher(ctx, daemonDiscordStatePath(), daemonDiscordStateWriteInterval, os.Getpid(), initialConfigErr)
	writerOptions = append(writerOptions, presence.WithConnectionState(statePublishers.connection))
	writerOptions = append(writerOptions, presence.WithPublicationState(statePublishers.publication))
	if verbose {
		writerOptions = append(writerOptions, presence.WithDebugf(debugf))
	}
	writer, err := presence.NewWriter(&presence.RichClient{}, presence.DefaultAppID, writerOptions...)
	if err != nil {
		return err
	}
	control.setReconnector(writer)

	detections := det.Run(ctx)
	usagePath := usagepkg.StatePath()
	usageStore, err := usagepkg.Load(usagePath)
	if err != nil {
		debugf("usage load skipped: %v", err)
	}
	if usageStore != nil {
		usageStore.Prune(registryToolIDs(applied.registry.Tools()), time.Now())
	}
	lastUsageSave := time.Time{}
	saveUsage := func(force bool) {
		if usageStore == nil {
			return
		}
		now := time.Now()
		if !force && !lastUsageSave.IsZero() && now.Sub(lastUsageSave) < 30*time.Second {
			return
		}
		if err := usagepkg.Save(usagePath, usageStore); err != nil {
			debugf("usage save skipped: %v", err)
			return
		}
		lastUsageSave = now
	}

	// Translate detector events into config-resolved activities. Display-only
	// reloads re-apply the current detection immediately; detector reloads scan
	// again with the new matching and selection settings.
	activities := make(chan *presence.Activity)
	go func() {
		defer close(activities)
		var (
			last           detector.Detection
			haveLast       bool
			haveGoodConfig = initialConfigErr == nil
		)
		send := func(a *presence.Activity) bool {
			select {
			case activities <- a:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			select {
			case detection, ok := <-detections:
				if !ok {
					return
				}
				last, haveLast = detection, true
				if detection.None {
					debugf("scan result: none")
				} else {
					debugf("scan result: featured=%s cwd=%s others=%s", detection.Tool.ID, debugDetectionDirectory(applied.config, detection), otherToolIDs(detection.Others))
					recordUsage(usageStore, detection, time.Now())
					saveUsage(false)
				}
				if !send(buildActivity(applied.config, detection, fallbackMessage)) {
					return
				}
			case reload := <-manager.Reloads():
				if reload.Err != nil {
					logConfigReloadFailure(reload.Err)
					statePublishers.config(reload.Err, haveGoodConfig)
					continue
				}
				next, change, err := applyConfigChange(applied, reload.Config)
				if err != nil {
					logConfigReloadFailure(err)
					statePublishers.config(err, haveGoodConfig)
					continue
				}
				if change.detector {
					if err := det.Reconfigure(ctx, next.registry, next.detectorConfig); err != nil {
						if ctx.Err() == nil {
							logConfigReloadFailure(err)
							statePublishers.config(err, haveGoodConfig)
						}
						continue
					}
				}
				applied = next
				haveGoodConfig = true
				statePublishers.config(nil, false)
				// A reload-introduced warning previously reached `termp
				// status` (a fresh config.LoadReadOnly() there recomputes the same
				// warnings) but never the daemon log, unlike a startup
				// warning logged via the same helper (issue #416 comment).
				// Routing both origins through logConfigWarnings keeps a
				// future call site from silently omitting the log.
				logConfigWarnings(next.config.Warnings)
				if usageStore != nil {
					usageStore.Prune(registryToolIDs(applied.registry.Tools()), time.Now())
				}
				debugf("config reloaded")
				if haveLast && !change.detector {
					if !send(buildActivity(applied.config, last, fallbackMessage)) {
						return
					}
				}
			case watchErr := <-manager.WatchErrors():
				logConfigWatchFailure(watchErr)
			case <-ctx.Done():
				return
			}
		}
	}()

	writer.RunActivities(ctx, activities)
	saveUsage(true)
	return nil
}

type detectionRuntime struct {
	config         config.Config
	registry       *registry.Registry
	detectorConfig detector.Config
}

type configChange struct {
	detector bool
	timing   bool
	registry bool
}

func newDetectionRuntime(cfg config.Config) (detectionRuntime, error) {
	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return detectionRuntime{}, err
	}
	return detectionRuntime{
		config:         cfg,
		registry:       reg,
		detectorConfig: detectorConfig(cfg),
	}, nil
}

// applyConfigChange prepares a reload transaction without mutating the current
// runtime. A registry compile failure therefore leaves all last-good behavior
// intact. Display/privacy-only changes reuse the existing detector and registry.
func applyConfigChange(current detectionRuntime, nextCfg config.Config) (detectionRuntime, configChange, error) {
	change := configChange{}
	next := detectionRuntime{
		config:         nextCfg,
		registry:       current.registry,
		detectorConfig: detectorConfig(nextCfg),
	}
	change.registry = !reflect.DeepEqual(current.config.CustomTools, nextCfg.CustomTools)
	if change.registry {
		reg, err := registry.NewWithCustom(nextCfg.CustomTools...)
		if err != nil {
			return current, configChange{}, err
		}
		next.registry = reg
	}
	change.timing = current.detectorConfig.ScanInterval != next.detectorConfig.ScanInterval
	change.detector = change.registry || !reflect.DeepEqual(current.detectorConfig, next.detectorConfig)
	return next, change, nil
}

func detectorConfig(cfg config.Config) detector.Config {
	disabledTools := make(map[string]bool)
	for id, override := range cfg.Tools {
		if override.Enabled != nil && !*override.Enabled {
			disabledTools[id] = true
		}
	}
	return detector.Config{
		ScanInterval:           cfg.ScanIntervalDuration(),
		IdleClearTimeout:       cfg.IdleClearTimeoutDuration(),
		Pin:                    cfg.Pin,
		HeadlinerIdleTimeout:   cfg.HeadlinerIdleTimeoutDuration(),
		CorroborateIdleWithCPU: detector.DefaultCorroborateIdleWithCPU,
		ActivitySwitching:      cfg.ActivitySwitching,
		DisabledTools:          disabledTools,
	}
}

func recordUsage(store *usagepkg.Store, detection detector.Detection, now time.Time) {
	if store == nil || detection.None {
		return
	}
	store.Record(detection.Tool.ID, now)
	for _, tool := range detection.Others {
		store.Record(tool.ID, now)
	}
}

func otherToolIDs(tools []registry.Tool) string {
	if len(tools) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(tools))
	for _, tool := range tools {
		ids = append(ids, tool.ID)
	}
	return strings.Join(ids, ",")
}

func debugDetectionDirectory(cfg config.Config, detection detector.Detection) string {
	resolved := cfg.Resolve(detection.Tool)
	if resolved.DirectoryAllowed(detection.Cwd) {
		return presence.DirectoryDisplay(detection.Cwd, resolved.DirectoryBasenameOnly)
	}
	return "hidden"
}

func registryToolIDs(tools []registry.Tool) []string {
	ids := make([]string, 0, len(tools))
	for _, tool := range tools {
		ids = append(ids, tool.ID)
	}
	return ids
}

// buildActivity resolves the config for a detection and produces the presence
// activity to display, or nil to clear presence. The directory (state) and
// buttons are decided here so the privacy allowlist and per-tool button
// overrides in internal/config are honored; internal/presence stays
// config-agnostic.
func buildActivity(cfg config.Config, detection detector.Detection, fallbackMessage string) *presence.Activity {
	if detection.None {
		return nil
	}
	resolved := cfg.Resolve(detection.Tool)
	if !resolved.Enabled {
		return nil
	}

	showDir := resolved.DirectoryAllowed(detection.Cwd)
	detection.Others = enabledOthers(cfg, detection.Others)
	if !showDir {
		detection.Cwd = ""
	}
	opts := presence.DisplayOptions{
		ToolName:              resolved.ToolName,
		DetailsFormat:         cfg.DetailsFormat,
		FallbackMessage:       fallbackMessage,
		ElapsedTimer:          resolved.ElapsedTimer,
		SmallImage:            resolved.SmallImage,
		Collection:            cfg.Display.Collection,
		ShowDirectory:         showDir,
		DirectoryBasenameOnly: resolved.DirectoryBasenameOnly,
	}
	activity, ok, omissions := presence.ActivityFromDetectionWithOmissions(detection, opts)
	if !ok {
		return nil
	}
	for _, omission := range omissions {
		debugf("%s", omission.Message())
	}
	if resolved.ButtonsEnabled {
		activity.Buttons = activityButtons(resolved.Buttons, cfg.CTA)
	}
	return &activity
}

func selectFallbackMessage(messages []string) string {
	if len(messages) == 0 {
		return config.BuiltInFallbackMessage
	}
	return messages[rand.IntN(len(messages))]
}

func enabledOthers(cfg config.Config, others []registry.Tool) []registry.Tool {
	if len(others) == 0 {
		return nil
	}
	filtered := make([]registry.Tool, 0, len(others))
	for _, tool := range others {
		if cfg.Resolve(tool).Enabled {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func activityButtons(buttons []registry.Button, cta config.CTA) []presence.Button {
	const maxButtons = 2
	out := make([]presence.Button, 0, maxButtons)
	for _, button := range buttons {
		if len(out) == maxButtons {
			return out
		}
		out = append(out, presence.Button{Label: button.Label, URL: button.URL})
	}
	if cta.Enabled && len(out) < maxButtons {
		out = append(out, presence.Button{Label: cta.Label, URL: cta.URL})
	}
	return out
}

func stop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp stop [--verbose]"); err != nil {
		return err
	}
	pidPath := pidFilePath()
	var publisher daemonPIDRecord
	if state, ok := readDaemonDiscordState(daemonDiscordStatePath()); ok &&
		processIdentityMatches(state.PID, state.StartTime, state.ExecutablePath, processAlive, processLooksLikeTermpAtPath) {
		publisher = daemonPIDRecord{PID: state.PID, StartTime: state.StartTime, ExecutablePath: state.ExecutablePath}
	}
	serviceState := service.NewManager().Status()
	pid, err := stopDaemonAndPublisher(pidPath, publisher, stopTimeout, stopPollInterval, processAlive, processLooksLikeTermpAtPath, signalTermpProcessAtPath, time.Sleep, serviceWillRelaunch(serviceState))
	if errors.Is(err, errUnreadablePIDFileRemoved) {
		fmt.Println(errUnreadablePIDFileRemoved.Error())
		return nil
	}
	if err != nil {
		return err
	}
	printStopSuccess(pid, serviceState)
	return nil
}

func stopRunningDaemon() (int, bool, error) {
	pidPath := pidFilePath()
	var publisher daemonPIDRecord
	if state, ok := readDaemonDiscordState(daemonDiscordStatePath()); ok &&
		processIdentityMatches(state.PID, state.StartTime, state.ExecutablePath, processAlive, processLooksLikeTermpAtPath) {
		publisher = daemonPIDRecord{PID: state.PID, StartTime: state.StartTime, ExecutablePath: state.ExecutablePath}
	}
	pid, err := stopDaemonAndPublisher(
		pidPath,
		publisher,
		stopTimeout,
		stopPollInterval,
		processAlive,
		processLooksLikeTermpAtPath,
		signalTermpProcessAtPath,
		time.Sleep,
		false,
	)
	if errors.Is(err, errDaemonNotRunning) || errors.Is(err, errUnreadablePIDFileRemoved) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return pid, true, nil
}

func printStopSuccess(pid int, state service.State) {
	fmt.Printf("stopped (pid %d)\n", pid)
	if serviceWillRelaunch(state) {
		fmt.Println("Autostart is on — run \"termp autostart disable\" to pause it (or \"termp autostart uninstall\" to remove autostart, not the binary).")
	}
}

func serviceWillRelaunch(state service.State) bool {
	if !state.Installed {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(state.Enabled)) {
	case "false", "disabled", "inactive":
		return false
	}
	switch strings.ToLower(strings.TrimSpace(state.Loaded)) {
	case "true", "active", "activating", "reloading", "running":
		return true
	default:
		return false
	}
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp status [--verbose]"); err != nil {
		return err
	}
	maybePrintFirstRunCTA(os.Stdout, config.DefaultPath(), isTerminal(os.Stdout))

	statusCtx, cancelStatus := context.WithTimeout(context.Background(), statusTimeout)
	defer cancelStatus()
	cfg, loadErr := loadConfigWithNotice(readOnlyConfigLoader, os.Stderr)
	defer printAvailableUpdateContext(statusCtx, cfg, loadErr)
	daemonPID := statusDaemonPID(pidFilePath(), daemonDiscordStatePath(), time.Now(), processAlive, processLooksLikeTermpAtPath)
	running := daemonPID > 0

	reg, registryErr := registry.NewWithCustom(cfg.CustomTools...)
	probes := runStatusProbes(statusCtx, statusProbeFuncs{
		daemonRunning: running,
		daemonPID:     daemonPID,
		discordState: func(now time.Time) (daemonDiscordState, bool) {
			return readFreshDaemonDiscordState(daemonDiscordStatePath(), now, daemonDiscordStateStaleAfter)
		},
		discord: func(ctx context.Context) error {
			return presence.StatusProbe(ctx, presence.DefaultAppID)
		},
		service: func(ctx context.Context) service.State {
			return service.NewManager().StatusContext(ctx)
		},
		tool: func(ctx context.Context) (detector.Detection, error) {
			if registryErr != nil {
				return detector.Detection{}, registryErr
			}
			if err := ctx.Err(); err != nil {
				return detector.Detection{}, err
			}
			return detector.ActiveDetectionWithPresence(reg, detector.NewGopsutilLister(), detector.Config{
				ScanInterval:           cfg.ScanIntervalDuration(),
				IdleClearTimeout:       cfg.IdleClearTimeoutDuration(),
				Pin:                    cfg.Pin,
				HeadlinerIdleTimeout:   cfg.HeadlinerIdleTimeoutDuration(),
				CorroborateIdleWithCPU: detector.DefaultCorroborateIdleWithCPU,
				ActivitySwitching:      cfg.ActivitySwitching,
			})
		},
	})
	serviceState := probes.service
	homeDir, _ := os.UserHomeDir()
	updateMethod := updatepkg.InstallGeneric
	if cfg.AutoUpdate && runtime.GOOS == "windows" {
		updateMethod = updatepkg.DetectInstallMethod()
	}
	daemonState, daemonStateOK := readFreshDaemonDiscordState(daemonDiscordStatePath(), time.Now(), daemonDiscordStateStaleAfter)
	configOK, configError, configUsingLastGood := statusConfigHealth(loadErr, daemonPID, daemonState, daemonStateOK)
	publicationRejected, publicationError, publicationAt := statusPublicationHealth(daemonPID, daemonState, daemonStateOK)
	info := statusInfo{
		running:             running,
		discord:             probes.discord,
		detectedTool:        probes.detectedTool,
		publicationRejected: publicationRejected,
		publicationError:    publicationError,
		publicationAt:       publicationAt,
		serviceSupported:    serviceState.Supported,
		serviceInstalled:    serviceState.Installed,
		serviceLoaded:       serviceState.Loaded,
		serviceEnabled:      serviceState.Enabled,
		servicePath:         serviceState.Path,
		servicePathLabel:    autostartLocationLabel(runtime.GOOS),
		serviceMessage:      serviceState.Message,
		configPath:          cfg.Path,
		configOK:            configOK,
		configError:         configError,
		configUsingLastGood: configUsingLastGood,
		configWarnings:      cfg.Warnings,
		updateFailure:       automaticUpdateStatus(updatepkg.DefaultCachePath(), cfg.AutoUpdate, version, runtime.GOOS, updateMethod),
		homeDir:             homeDir,
	}

	if registryErr != nil {
		fmt.Print(formatStatus(info))
		return registryErr
	}
	fmt.Print(formatStatus(info))
	return nil
}

func statusDaemonPID(pidPath, discordStatePath string, now time.Time, alive func(int) bool, looksLikeTermp func(int, string) bool) int {
	if record, _, err := readPIDIdentity(pidPath); err == nil &&
		pidRecordIdentityMatches(record, alive, looksLikeTermp) {
		return record.PID
	}
	if state, ok := readFreshDaemonDiscordState(discordStatePath, now, daemonDiscordStateStaleAfter); ok &&
		processIdentityMatches(state.PID, state.StartTime, state.ExecutablePath, alive, looksLikeTermp) {
		return state.PID
	}
	return 0
}

func statusConfigHealth(loadErr error, daemonPID int, state daemonDiscordState, stateOK bool) (bool, error, bool) {
	configOK := loadErr == nil
	configError := loadErr
	usingLastGood := false
	if !stateOK || daemonPID <= 0 || state.PID != daemonPID || state.ConfigOK == nil {
		return configOK, configError, usingLastGood
	}
	if !*state.ConfigOK {
		configOK = false
		if state.ConfigError != "" {
			configError = errors.New(state.ConfigError)
		}
		usingLastGood = state.ConfigUsingLastGood
	} else if loadErr != nil {
		// The invalid file may be newer than the daemon's latest watch event.
		// Until it processes that event, its previously valid config is active.
		usingLastGood = true
	}
	return configOK, configError, usingLastGood
}

// statusPublicationHealth reports the daemon's last publication result,
// separate from Discord connection health: a healthy IPC connection can
// still have its most recent activity permanently rejected (classic IPC code
// 4000). It only trusts a fresh state record from the currently running
// daemon, and reports "not rejected" once that daemon has recorded a
// successful publish or cleared presence after any earlier rejection.
func statusPublicationHealth(daemonPID int, state daemonDiscordState, stateOK bool) (bool, string, time.Time) {
	if !stateOK || daemonPID <= 0 || state.PID != daemonPID || state.PublicationOK == nil || *state.PublicationOK {
		return false, "", time.Time{}
	}
	var at time.Time
	if state.PublicationAt != nil {
		at = *state.PublicationAt
	}
	return true, state.PublicationError, at
}

type statusProbeFuncs struct {
	daemonRunning bool
	daemonPID     int
	discordState  func(time.Time) (daemonDiscordState, bool)
	now           func() time.Time
	discord       func(context.Context) error
	service       func(context.Context) service.State
	tool          func(context.Context) (detector.Detection, error)
}

type statusProbeResults struct {
	discord      string
	service      service.State
	detectedTool string
}

type statusStageResult struct {
	stage        string
	discord      string
	service      service.State
	detectedTool string
}

func runStatusProbes(ctx context.Context, probes statusProbeFuncs) statusProbeResults {
	results := statusProbeResults{
		discord: "unknown (probe timed out)",
		service: service.State{
			Supported: runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows",
			Loaded:    "unknown",
			Enabled:   "unknown",
			Message:   "status probe timed out",
		},
		detectedTool: "unknown (probe timed out)",
	}
	resultCh := make(chan statusStageResult, 3)
	now := probes.now
	if now == nil {
		now = time.Now
	}

	go func() {
		if probes.daemonRunning && probes.discordState != nil {
			if state, ok := probes.discordState(now()); ok {
				if state.Connected {
					discord := "connected"
					if probes.daemonPID > 0 && state.PID > 0 && state.PID != probes.daemonPID {
						discord = fmt.Sprintf("connected (another termp daemon owns Discord: pid %d; PID file names %d)", state.PID, probes.daemonPID)
					}
					resultCh <- statusStageResult{stage: "discord", discord: discord}
					return
				}
			}
		}
		err := probes.discord(ctx)
		resultCh <- statusStageResult{stage: "discord", discord: formatDiscordStatus(err)}
	}()
	go func() {
		resultCh <- statusStageResult{stage: "service", service: probes.service(ctx)}
	}()
	go func() {
		detection, err := probes.tool(ctx)
		value := "none"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			value = "unknown (probe timed out)"
		} else if err != nil {
			value = fmt.Sprintf("unknown (%v)", err)
		} else if !detection.None {
			value = detection.Tool.ID
		}
		resultCh <- statusStageResult{stage: "tool", detectedTool: value}
	}()

	for completed := 0; completed < 3; {
		select {
		case result := <-resultCh:
			applyStatusStageResult(&results, result)
			completed++
		case <-ctx.Done():
			for {
				select {
				case result := <-resultCh:
					applyStatusStageResult(&results, result)
				default:
					return results
				}
			}
		}
	}
	return results
}

type daemonDiscordState struct {
	Connected           bool      `json:"connected"`
	UpdatedAt           time.Time `json:"updated_at"`
	PID                 int       `json:"pid"`
	StartTime           uint64    `json:"start_time,omitempty"`
	ExecutablePath      string    `json:"executable_path,omitempty"`
	ConfigOK            *bool     `json:"config_ok,omitempty"`
	ConfigError         string    `json:"config_error,omitempty"`
	ConfigUsingLastGood bool      `json:"config_using_last_good,omitempty"`
	// PublicationOK records the outcome of the most recent activity
	// publication attempt, separate from Connected (transport health).
	// Discord IPC can permanently reject a payload (classic IPC code 4000)
	// while the connection stays healthy, so status must be able to tell
	// "connected" apart from "published". It clears (returns to nil/"")
	// the moment a later publish succeeds or presence is cleared; see
	// presence.WithPublicationState. PublicationAt is a pointer (not a bare
	// time.Time) because encoding/json's omitempty never omits a zero-value
	// struct field, so a bare time.Time would always render as
	// "0001-01-01T00:00:00Z" before anything was ever published.
	PublicationOK    *bool      `json:"publication_ok,omitempty"`
	PublicationError string     `json:"publication_error,omitempty"`
	PublicationAt    *time.Time `json:"publication_at,omitempty"`
}

func daemonDiscordStatePath() string {
	return filepath.Join(filepath.Dir(usagepkg.StatePath()), "discord.json")
}

type daemonStatePublishers struct {
	connection  func(bool)
	config      func(error, bool)
	publication func(error)
}

func runDaemonDiscordStatePublisher(ctx context.Context, path string, interval time.Duration, pid int, initialConfigErr error) daemonStatePublishers {
	startTime, _ := processStartTime(pid)
	executablePath, _ := currentProcessExecutablePath()
	configOK := initialConfigErr == nil
	state := daemonDiscordState{
		PID:            pid,
		StartTime:      startTime,
		ExecutablePath: executablePath,
		ConfigOK:       &configOK,
		ConfigError:    errorString(initialConfigErr),
	}
	updates := make(chan daemonDiscordState, 1)
	var stateMu sync.Mutex
	publish := func(update func(*daemonDiscordState)) {
		stateMu.Lock()
		update(&state)
		snapshot := state
		select {
		case updates <- snapshot:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- snapshot:
			default:
			}
		}
		stateMu.Unlock()
	}
	go func() {
		writeState := func(snapshot daemonDiscordState) {
			snapshot.UpdatedAt = time.Now()
			_ = writeDaemonDiscordState(path, snapshot)
		}
		stateMu.Lock()
		initial := state
		stateMu.Unlock()
		writeState(initial)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case snapshot := <-updates:
				writeState(snapshot)
			case <-ticker.C:
				stateMu.Lock()
				snapshot := state
				stateMu.Unlock()
				writeState(snapshot)
			case <-ctx.Done():
				return
			}
		}
	}()
	return daemonStatePublishers{
		connection: func(connected bool) {
			publish(func(state *daemonDiscordState) {
				state.Connected = connected
			})
		},
		config: func(err error, usingLastGood bool) {
			publish(func(state *daemonDiscordState) {
				ok := err == nil
				state.ConfigOK = &ok
				state.ConfigError = errorString(err)
				state.ConfigUsingLastGood = err != nil && usingLastGood
			})
		},
		publication: func(err error) {
			publish(func(state *daemonDiscordState) {
				ok := err == nil
				at := time.Now()
				state.PublicationOK = &ok
				state.PublicationError = errorString(err)
				state.PublicationAt = &at
			})
		},
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeDaemonDiscordState(path string, state daemonDiscordState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readFreshDaemonDiscordState(path string, now time.Time, staleAfter time.Duration) (daemonDiscordState, bool) {
	state, ok := readDaemonDiscordState(path)
	if !ok {
		return daemonDiscordState{}, false
	}
	if state.UpdatedAt.IsZero() || staleAfter <= 0 || now.Sub(state.UpdatedAt) > staleAfter {
		return daemonDiscordState{}, false
	}
	return state, true
}

func readDaemonDiscordState(path string) (daemonDiscordState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return daemonDiscordState{}, false
	}
	var state daemonDiscordState
	if err := json.Unmarshal(data, &state); err != nil {
		return daemonDiscordState{}, false
	}
	return state, true
}

func knownDaemonRecord(pidPath, discordStatePath string, alive func(int) bool, looksLikeTermp func(int, string) bool) daemonPIDRecord {
	if record, _, err := readPIDIdentity(pidPath); err == nil &&
		pidRecordIdentityMatches(record, alive, looksLikeTermp) {
		return record
	}
	if state, ok := readDaemonDiscordState(discordStatePath); ok &&
		processIdentityMatches(state.PID, state.StartTime, state.ExecutablePath, alive, looksLikeTermp) {
		return daemonPIDRecord{PID: state.PID, StartTime: state.StartTime, ExecutablePath: state.ExecutablePath}
	}
	return daemonPIDRecord{}
}

func knownDaemonPID(pidPath, discordStatePath string, alive func(int) bool, looksLikeTermp func(int, string) bool) int {
	return knownDaemonRecord(pidPath, discordStatePath, alive, looksLikeTermp).PID
}

func executablePathsMatch(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func daemonAlreadyRunningError(record daemonPIDRecord, currentPath string) error {
	if record.ExecutablePath != "" && !executablePathsMatch(record.ExecutablePath, currentPath) {
		return fmt.Errorf("a termp daemon is already running (pid %d), launched from %q; stop it first with 'termp stop'", record.PID, record.ExecutablePath)
	}
	return fmt.Errorf("daemon already running with pid %d", record.PID)
}

var lookupProcessStartTime = processStartTime

func processIdentityMatches(pid int, expectedStartTime uint64, expectedPath string, alive func(int) bool, looksLikeTermp func(int, string) bool) bool {
	if pid <= 0 || !alive(pid) || !looksLikeTermp(pid, expectedPath) {
		return false
	}
	if expectedStartTime == 0 {
		return false
	}
	actualStartTime, err := lookupProcessStartTime(pid)
	if err != nil {
		return true
	}
	return actualStartTime == expectedStartTime
}

// formatDiscordStatus reports what the probe actually established, not more.
// Each branch above the default asserts something concrete because the
// matched sentinel error establishes it (e.g. ErrDiscordIPCNotFound means no
// endpoint was found at all, so "not running" is warranted). The fallback
// below previously repeated the ErrDiscordIPCUnreachable wording —
// "Discord is running but unreachable" — for *any* unmatched error,
// including ones that say nothing about Discord running at all (issue
// flagged during the PR #423 review: a malformed DISCORD_IPC_PATH override
// would render this way too, which is actively misleading). An unrecognized
// error only proves the probe couldn't determine Discord's state; it must
// not claim the more specific "running but unreachable" diagnosis it never
// established.
func formatDiscordStatus(err error) string {
	if err == nil {
		return "connected"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "unknown (probe timed out)"
	}
	if errors.Is(err, presence.ErrDiscordIPCOverrideInvalid) {
		return "misconfigured (DISCORD_IPC_PATH override is invalid)"
	}
	if errors.Is(err, presence.ErrDiscordIPCNotFound) {
		return "not running (start Discord to show presence)"
	}
	if errors.Is(err, presence.ErrDiscordIPCUnreachable) {
		return "connection failed (Discord is running but unreachable)"
	}
	if errors.Is(err, presence.ErrDiscordIPCHandshakeTimeout) {
		return "not responding (Discord IPC handshake timed out)"
	}
	return fmt.Sprintf("unknown (%v)", err)
}

func applyStatusStageResult(results *statusProbeResults, result statusStageResult) {
	switch result.stage {
	case "discord":
		results.discord = result.discord
	case "service":
		results.service = result.service
	case "tool":
		results.detectedTool = result.detectedTool
	}
}

type statusInfo struct {
	running             bool
	discord             string
	detectedTool        string
	publicationRejected bool
	publicationError    string
	publicationAt       time.Time
	serviceSupported    bool
	serviceInstalled    bool
	serviceLoaded       string
	serviceEnabled      string
	servicePath         string
	servicePathLabel    string
	serviceMessage      string
	configPath          string
	configOK            bool
	configError         error
	configUsingLastGood bool
	configWarnings      []string
	updateFailure       string
	homeDir             string
}

func formatStatus(info statusInfo) string {
	autostart := []outputField{
		{label: "Supported", value: yesNo(info.serviceSupported)},
		{label: "Installed", value: yesNo(info.serviceInstalled)},
		{label: "Loaded", value: humanizeState(info.serviceLoaded)},
		{label: "Enabled", value: humanizeState(info.serviceEnabled)},
	}
	if info.servicePath != "" {
		label := info.servicePathLabel
		if label == "" {
			label = "Path"
		}
		autostart = append(autostart, outputField{label: label, value: abbreviateHome(info.servicePath, info.homeDir)})
	}
	if info.serviceMessage != "" {
		autostart = append(autostart, outputField{label: "Message", value: info.serviceMessage})
	}

	configFields := []outputField{
		{label: "Path", value: abbreviateHome(info.configPath, info.homeDir)},
		{label: "Valid", value: yesNo(info.configOK)},
	}
	if info.configError != nil {
		presenceState := "off (invalid config)"
		if info.configUsingLastGood {
			presenceState = "using last-good config"
		}
		configFields = append(configFields, outputField{label: "Presence", value: presenceState})
		configFields = append(configFields, outputField{label: "Error", value: info.configError.Error()})
	}
	for _, warning := range info.configWarnings {
		configFields = append(configFields, outputField{label: "Warning", value: warning})
	}

	daemonFields := []outputField{
		{label: "Running", value: yesNo(info.running)},
		{label: "Discord", value: info.discord},
		{label: "Detected tool", value: info.detectedTool},
	}
	if info.publicationRejected {
		daemonFields = append(daemonFields, outputField{
			label: "Published",
			value: fmt.Sprintf("rejected at %s: %s", info.publicationAt.Local().Format(time.RFC3339), info.publicationError),
		})
	}

	sections := []outputSection{
		outputSection{header: "Daemon", fields: daemonFields},
		outputSection{header: "Autostart", fields: autostart},
		outputSection{header: "Config", fields: configFields},
	}
	if info.updateFailure != "" {
		sections = append(sections, outputSection{header: "Updates", fields: []outputField{
			{label: "Automatic", value: info.updateFailure},
		}})
	}
	return formatSections("termp status", sections...)
}

// automaticUpdateFailure renders the cached automatic-update attempt, if any
// is still actionable. A recorded attempt is stale — and suppressed — once
// current is no longer behind the attempted target: that covers both a later
// automatic success and a manual update (`termp update`, brew upgrade, a
// package manager) that independently reached the same or a newer version.
func automaticUpdateFailure(path, current string) string {
	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(path)
	if !ok || attempt.Error == "" || !updatepkg.IsNewer(current, attempt.Target) {
		return ""
	}
	outcome := "failed"
	if attempt.Skipped {
		outcome = "skipped"
	}
	return fmt.Sprintf("%s for %s at %s: %s; run `termp update` manually",
		outcome,
		attempt.Target,
		attempt.AttemptedAt.Local().Format(time.RFC3339),
		attempt.Error,
	)
}

// automaticUpdateStatus reports the automatic-update section for `termp
// status`. It renders nothing when automatic updates are disabled: the
// "Automatic" line describes automatic-update behavior, and a stale failure
// from before the user turned it off is no longer actionable advice about a
// feature that is not running.
func automaticUpdateStatus(path string, enabled bool, current, goos string, method updatepkg.InstallMethod) string {
	if !enabled {
		return ""
	}
	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(path)
	var target []string
	if ok {
		target = []string{attempt.Target}
	}
	if err := automaticUpdatePlatformPreflight(goos, method, target...); err != nil {
		if ok && attempt.Skipped && attempt.Error == err.Error() {
			return automaticUpdateFailure(path, current)
		}
		return fmt.Sprintf("skipped: %s", err)
	}
	return automaticUpdateFailure(path, current)
}

func autostartLocationLabel(goos string) string {
	if goos == "windows" {
		return "Task"
	}
	return "Path"
}

func abbreviateHome(path, homeDir string) string {
	if homeDir == "" || path == "" {
		return path
	}
	cleanPath := filepath.Clean(path)
	cleanHome := filepath.Clean(homeDir)
	if cleanPath == cleanHome {
		return "~"
	}
	if strings.HasPrefix(cleanPath, cleanHome+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(cleanPath, cleanHome)
	}
	return path
}

func settings(args []string) error {
	terminal := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	return settingsWithConfigLoader(args, terminal, config.Load)
}

// settingsLoadRecovery decides how the settings command should react to a
// config load error (#475). It returns an optional banner notice to show in the
// editor, and a fatal error the command should return instead of opening.
//
//   - No error: no notice, open normally.
//   - ErrConfigBeingWritten: fatal. A whole-document write is in flight;
//     opening now could overwrite it from a partial read, so ask the user to
//     retry rather than editing against a guess (config.md, #438). The on-disk
//     bytes are left untouched.
//   - Any other error (an invalid value such as scan_interval = "5" with no
//     unit, undecodable TOML, or an unreadable file): NOT fatal. Exiting 1
//     here would leave settings -- the only tool that can repair the config --
//     unreachable while every load fails closed with presence off, which is the
//     lock-out this issue exists to fix. The caller opens the editor against the
//     fail-closed fallback config (safe defaults, presence disabled) and shows
//     the returned notice, which names the problem. Saving replaces the whole
//     file, so any other authored values in the unloadable file are lost --
//     disclosed in the notice because the invalid file could not be decoded to
//     preserve them.
func settingsLoadRecovery(loadErr error) (notice string, fatal error) {
	if loadErr == nil {
		return "", nil
	}
	if errors.Is(loadErr, config.ErrConfigBeingWritten) {
		return "", loadErr
	}
	return fmt.Sprintf("Could not load your config, so safe defaults are shown (presence is off until it is valid): %v. Fix the value and save to write a valid config; this overwrites the current file.", loadErr), nil
}

func settingsWithConfigLoader(args []string, terminal bool, load func() (config.Config, error)) error {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp settings [--verbose]"); err != nil {
		return err
	}
	if !terminal {
		fmt.Fprintln(os.Stderr, "termp settings requires an interactive terminal (TTY)")
		return errors.New("settings requires a TTY")
	}
	cfg, loadErr := loadConfigWithNotice(load, os.Stderr)
	notice, fatal := settingsLoadRecovery(loadErr)
	if fatal != nil {
		return fatal
	}
	printCommandUpdateAlert("settings", args, isTerminal(os.Stderr), cfg, loadErr, os.Stderr)
	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return err
	}
	usageStore, err := usagepkg.Load(usagepkg.StatePath())
	if err != nil {
		debugf("usage load skipped: %v", err)
		usageStore = usagepkg.New()
	}
	usageStore.Prune(registryToolIDs(reg.Tools()), time.Now())
	model := tui.NewSettingsModel(cfg, reg.Tools(), usageStore.Rank(), func(next config.Config) error {
		return config.Save(next, config.DefaultPath())
	}, openInBrowser).WithNotice(notice)
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	addVerboseFlag(fs)
	once := fs.Bool("once", false, "render one preview snapshot and exit")
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp watch [--once] [--verbose]"); err != nil {
		return err
	}
	if *once {
		card, warnings, err := watchSnapshot(time.Now())
		if err != nil {
			return err
		}
		logConfigWarnings(warnings)
		fmt.Println(card)
		return nil
	}
	maybePrintFirstRunCTA(os.Stdout, config.DefaultPath(), isTerminal(os.Stdout))
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr, "termp watch requires an interactive terminal (TTY); use --once for scripting")
		return errors.New("watch requires a TTY")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	configPath := config.DefaultPath()
	manager, cfg, loadErr := newWatchedConfigManager(configPath, func(manager *config.Manager) {
		if err := config.EnsureConfigDir(configPath); err != nil {
			log.Printf("config watch disabled: %v", err)
		} else if err := manager.Watch(ctx); err != nil {
			log.Printf("config watch disabled: %v", err)
		}
	})
	logConfigWarnings(cfg.Warnings)

	applied, err := newDetectionRuntime(cfg)
	if err != nil {
		return err
	}
	det, err := detector.New(applied.registry, detector.NewGopsutilLister(), applied.detectorConfig)
	if err != nil {
		return err
	}

	model := tui.NewWatchModelWithConfig(cfg, time.Now)
	if loadErr != nil {
		model.SetWarning(configLoadFallbackWarning(loadErr))
	}
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
	detections := det.RunReadOnly(ctx)

	go bridgeWatchActivities(ctx, manager, det, applied, detections, program, selectFallbackMessage(cfg.FallbackMessages))
	go bridgeWatchConnection(ctx, program, 5*time.Second)

	_, err = program.Run()
	cancel()
	return err
}

func watchSnapshot(now time.Time) (string, []string, error) {
	cfg, loadErr := config.LoadReadOnly()
	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return "", nil, err
	}
	detection, err := detector.ActiveDetectionWithPresence(reg, detector.NewGopsutilLister(), detector.Config{
		ScanInterval:           cfg.ScanIntervalDuration(),
		IdleClearTimeout:       cfg.IdleClearTimeoutDuration(),
		Pin:                    cfg.Pin,
		HeadlinerIdleTimeout:   cfg.HeadlinerIdleTimeoutDuration(),
		CorroborateIdleWithCPU: detector.DefaultCorroborateIdleWithCPU,
		ActivitySwitching:      cfg.ActivitySwitching,
	})
	if err != nil {
		debugf("watch snapshot scan skipped: %v", err)
		detection = detector.Detection{None: true}
	}
	connected := watchDiscordConnected(now, func() error {
		return presence.Probe(presence.DefaultAppID)
	})
	activity := buildActivity(cfg, detection, selectFallbackMessage(cfg.FallbackMessages))
	recent := []tui.RecentDetection(nil)
	if activity != nil && detection.Tool.DisplayName != "" {
		recent = []tui.RecentDetection{{Name: detection.Tool.DisplayName, At: now}}
	}
	warnings := cfg.Warnings
	if loadErr != nil {
		warnings = append([]string{configLoadFallbackWarning(loadErr)}, warnings...)
	}
	return tui.RenderCard(tui.CardState{
		Activity:  activity,
		Connected: connected,
		Now:       now,
		Recent:    recent,
	}, tui.DefaultCardStyles(cfg.UI.AccentColor)), warnings, nil
}

func configLoadFallbackWarning(err error) string {
	return fmt.Sprintf(`config load failed; presence is off until the config is valid; run "termp status" for details: %v`, err)
}

type detectorReconfigurer interface {
	Reconfigure(context.Context, *registry.Registry, detector.Config) error
}

func bridgeWatchActivities(ctx context.Context, manager *config.Manager, det detectorReconfigurer, applied detectionRuntime, detections <-chan detector.Detection, program *tea.Program, fallbackMessage string) {
	bridgeWatchActivityUpdates(ctx, manager.Reloads(), manager.WatchErrors(), det, applied, detections, func(cfg config.Config, detection detector.Detection) {
		activity := buildActivity(cfg, detection, fallbackMessage)
		name := ""
		if activity != nil {
			name = detection.Tool.DisplayName
		}
		program.Send(tui.ActivityMsg{Activity: activity, FeaturedName: name})
	}, func(warning string) {
		program.Send(tui.WarningMsg(warning))
	})
}

func bridgeWatchActivityUpdates(ctx context.Context, reloads <-chan config.ReloadResult, watchErrs <-chan error, det detectorReconfigurer, applied detectionRuntime, detections <-chan detector.Detection, send func(config.Config, detector.Detection), warn func(string)) {
	var (
		last     detector.Detection
		haveLast bool
	)
	for {
		select {
		case detection, ok := <-detections:
			if !ok {
				return
			}
			last, haveLast = detection, true
			send(applied.config, detection)
		case reload := <-reloads:
			if reload.Err != nil {
				warn(configReloadFailure(reload.Err))
				continue
			}
			next, change, err := applyConfigChange(applied, reload.Config)
			if err != nil {
				warn(configReloadFailure(err))
				continue
			}
			if change.detector {
				if err := det.Reconfigure(ctx, next.registry, next.detectorConfig); err != nil {
					if ctx.Err() == nil {
						warn(configReloadFailure(err))
					}
					continue
				}
			}
			applied = next
			warn("")
			debugf("config reloaded")
			if haveLast && !change.detector {
				send(applied.config, last)
			}
		case watchErr := <-watchErrs:
			warn(configWatchFailure(watchErr))
		case <-ctx.Done():
			return
		}
	}
}

func bridgeWatchConnection(ctx context.Context, program *tea.Program, interval time.Duration) {
	send := func() {
		connected := watchDiscordConnected(time.Now(), func() error {
			return presence.Probe(presence.DefaultAppID)
		})
		program.Send(tui.ConnMsg(connected))
	}
	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			send()
		case <-ctx.Done():
			return
		}
	}
}

func watchDiscordConnected(now time.Time, probe func() error) bool {
	return watchDiscordConnectedWith(
		now,
		pidFilePath(),
		daemonDiscordStatePath(),
		processAlive,
		processLooksLikeTermpAtPath,
		probe,
	)
}

func watchDiscordConnectedWith(
	now time.Time,
	pidPath string,
	discordStatePath string,
	alive func(int) bool,
	looksLikeTermp func(int, string) bool,
	probe func() error,
) bool {
	record := knownDaemonRecord(pidPath, discordStatePath, alive, looksLikeTermp)
	if record.PID > 0 {
		state, ok := readFreshDaemonDiscordState(discordStatePath, now, daemonDiscordStateStaleAfter)
		return discordConnectedFromStateOrProbe(record.PID, state, ok, probe)
	}
	return probe() == nil
}

func daemonDiscordStateConnected(daemonPID int, state daemonDiscordState) bool {
	return state.Connected
}

func discordConnectedFromStateOrProbe(daemonPID int, state daemonDiscordState, fresh bool, probe func() error) bool {
	if fresh && daemonDiscordStateConnected(daemonPID, state) {
		return true
	}
	return probe() == nil
}

func openInBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	default:
		return exec.Command("xdg-open", url).Run()
	}
}

func pidFilePath() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "termp.pid")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "termp", "run", "termp.pid")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("termp-%d", os.Geteuid()), "termp.pid")
}

func readPID(path string) (int, error) {
	pid, _, err := readPIDRecord(path)
	return pid, err
}

type daemonPIDRecord struct {
	PID                  int    `json:"pid"`
	StartTime            uint64 `json:"start_time,omitempty"`
	StartTimeUnavailable bool   `json:"start_time_unavailable,omitempty"`
	ExecutablePath       string `json:"executable_path,omitempty"`
}

func pidRecordIdentityMatches(record daemonPIDRecord, alive func(int) bool, looksLikeTermp func(int, string) bool) bool {
	if record.StartTimeUnavailable {
		return record.PID > 0 && alive(record.PID) && looksLikeTermp(record.PID, record.ExecutablePath)
	}
	return processIdentityMatches(record.PID, record.StartTime, record.ExecutablePath, alive, looksLikeTermp)
}

func readPIDRecord(path string) (int, os.FileInfo, error) {
	record, info, err := readPIDIdentity(path)
	return record.PID, info, err
}

func readPIDIdentity(path string) (daemonPIDRecord, os.FileInfo, error) {
	file, err := openValidatedPIDFile(path)
	if err != nil {
		return daemonPIDRecord{}, nil, err
	}
	defer file.Close()
	return readPIDIdentityFromFile(file)
}

func readPIDRecordFromFile(file *os.File) (int, os.FileInfo, error) {
	record, info, err := readPIDIdentityFromFile(file)
	return record.PID, info, err
}

func readPIDIdentityFromFile(file *os.File) (daemonPIDRecord, os.FileInfo, error) {
	data, err := io.ReadAll(file)
	if err != nil {
		return daemonPIDRecord{}, nil, err
	}
	record, err := parsePIDRecord(data)
	if err != nil {
		return daemonPIDRecord{}, nil, fmt.Errorf("%w: %v", errPIDRecordUnparseable, err)
	}
	info, err := file.Stat()
	if err != nil {
		return daemonPIDRecord{}, nil, err
	}
	return record, info, nil
}

func parsePIDRecord(data []byte) (daemonPIDRecord, error) {
	trimmed := strings.TrimSpace(string(data))
	if pid, err := strconv.Atoi(trimmed); err == nil {
		if pid <= 0 {
			return daemonPIDRecord{}, fmt.Errorf("invalid PID %d", pid)
		}
		return daemonPIDRecord{PID: pid}, nil
	}
	var record daemonPIDRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return daemonPIDRecord{}, err
	}
	if record.PID <= 0 {
		return daemonPIDRecord{}, fmt.Errorf("invalid PID %d", record.PID)
	}
	if record.StartTimeUnavailable && record.StartTime != 0 {
		return daemonPIDRecord{}, errors.New("PID record has both a start time and an unavailable marker")
	}
	return record, nil
}

func writePID(path string, pid int) error {
	_, err := writePIDOwned(path, pid)
	return err
}

func writePIDOwned(path string, pid int) (os.FileInfo, error) {
	return writePIDOwnedWithHook(path, pid, nil)
}

var pendingPIDFileSequence atomic.Uint64

func createPendingPIDFile(path string) (*os.File, string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		pendingPath := fmt.Sprintf("%s.pending-%d-%d", path, os.Getpid(), pendingPIDFileSequence.Add(1))
		file, err := createPIDFile(pendingPath)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, pendingPath, err
	}
	return nil, "", errors.New("cannot allocate pending PID file")
}

func writePIDOwnedWithHook(path string, pid int, initializingHook func()) (os.FileInfo, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid PID %d", pid)
	}
	if err := ensurePIDDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	record := daemonPIDRecord{PID: pid}
	executablePath, err := currentProcessExecutablePath()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	record.ExecutablePath = executablePath
	startTime, startTimeErr := lookupProcessStartTime(pid)
	if startTimeErr != nil || startTime == 0 {
		record.StartTimeUnavailable = true
	} else {
		record.StartTime = startTime
	}
	recordData, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	recordData = append(recordData, '\n')

	for {
		file, pendingPath, err := createPendingPIDFile(path)
		if err != nil {
			return nil, err
		}
		published := false
		cleanup := func() {
			_ = file.Close()
			if !published {
				_ = os.Remove(pendingPath)
			}
		}
		if err := file.Chmod(0o600); err != nil {
			cleanup()
			return nil, err
		}
		if err := lockPIDFile(file); err != nil {
			cleanup()
			return nil, fmt.Errorf("lock pending PID file: %w", err)
		}
		if initializingHook != nil {
			initializingHook()
		}
		if _, err := file.Write(recordData); err != nil {
			cleanup()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			cleanup()
			return nil, err
		}
		if err := publishPIDFile(pendingPath, path); err == nil {
			published = true
			info, err := file.Stat()
			cleanup()
			return info, err
		} else if !errors.Is(err, os.ErrExist) {
			cleanup()
			return nil, err
		}
		cleanup()

		// Only replace a stale file after proving that it is an owned regular
		// file. Its lock serializes initialization and stale replacement.
		existing, openErr := openValidatedPIDFile(path)
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			return nil, openErr
		}
		if lockErr := lockPIDFile(existing); lockErr != nil {
			existing.Close()
			return nil, fmt.Errorf("PID file is busy: %w", lockErr)
		}
		existingRecord, _, readErr := readPIDIdentityFromFile(existing)
		existingInfo, statErr := existing.Stat()
		if statErr != nil {
			existing.Close()
			return nil, statErr
		}
		if readErr == nil && pidRecordIdentityMatches(existingRecord, processAlive, processLooksLikeTermpAtPath) {
			existing.Close()
			return nil, fmt.Errorf("daemon already running with pid %d", existingRecord.PID)
		}
		currentInfo, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			existing.Close()
			return nil, lstatErr
		}
		if !os.SameFile(existingInfo, currentInfo) {
			existing.Close()
			return nil, errors.New("PID file changed while being replaced")
		}
		if removeErr := os.Remove(path); removeErr != nil {
			existing.Close()
			return nil, removeErr
		}
		if closeErr := existing.Close(); closeErr != nil {
			return nil, closeErr
		}
		// Loop to stage, lock, initialize, and atomically publish the replacement.
		// No contender can observe an empty authoritative file.
	}
}

func ensurePIDDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("PID directory %q is not a directory", path)
	}
	if err := requireCurrentUserOwner(info, "PID directory"); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func openValidatedPIDFile(path string) (*os.File, error) {
	file, err := openPIDFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := validatePIDFileInfo(info, path); err != nil {
		file.Close()
		return nil, err
	}
	if err := validatePIDFileHandle(file, path); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func validatePIDFileInfo(info os.FileInfo, path string) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("PID file %q is not a regular file", path)
	}
	return requireCurrentUserOwner(info, "PID file")
}

func pidFileMatchesOwner(expectedPID, actualPID int, expectedInfo, actualInfo os.FileInfo) bool {
	return expectedPID > 0 && expectedPID == actualPID && expectedInfo != nil && actualInfo != nil && os.SameFile(expectedInfo, actualInfo)
}

type pidRemovalResult uint8

const (
	pidRemovalChanged pidRemovalResult = iota
	pidRemovalRemoved
	pidRemovalAbsent
)

func removePIDIfOwnedResult(path string, expectedPID int, expectedInfo os.FileInfo) (pidRemovalResult, error) {
	file, err := openValidatedPIDFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return pidRemovalAbsent, nil
	}
	if err != nil {
		return pidRemovalChanged, err
	}
	defer file.Close()
	if err := lockPIDFile(file); err != nil {
		return pidRemovalChanged, fmt.Errorf("PID file is busy: %w", err)
	}
	actualPID, actualInfo, err := readPIDRecordFromFile(file)
	if err != nil {
		return pidRemovalChanged, err
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pidRemovalAbsent, nil
		}
		return pidRemovalChanged, err
	}
	if !pidFileMatchesOwner(expectedPID, actualPID, expectedInfo, actualInfo) || !os.SameFile(actualInfo, currentInfo) {
		return pidRemovalChanged, nil
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pidRemovalAbsent, nil
		}
		return pidRemovalChanged, err
	}
	return pidRemovalRemoved, nil
}

func removePIDIfOwned(path string, expectedPID int, expectedInfo os.FileInfo) (bool, error) {
	result, err := removePIDIfOwnedResult(path, expectedPID, expectedInfo)
	return result == pidRemovalRemoved, err
}

// removeUnreadablePIDFile best-effort removes a PID file whose contents
// could not be parsed (see errPIDRecordUnparseable). Unlike
// removePIDIfOwned, there is no known expected PID/identity to compare
// against — the file never yielded one — so ownership is instead proven by
// holding an exclusive lock on the already-owner/regular-file-validated
// handle and re-checking that the path still refers to that same file
// immediately before removing it. If the file changed underneath us (for
// example a fresh daemon just replaced it with a valid record), it is left
// alone.
func removeUnreadablePIDFile(path string) error {
	file, err := openValidatedPIDFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()
	if err := lockPIDFile(file); err != nil {
		return fmt.Errorf("PID file is busy: %w", err)
	}
	if _, _, err := readPIDIdentityFromFile(file); err == nil {
		// The file became parseable while we were investigating it (a
		// concurrent writer replaced it); leave it alone rather than
		// deleting what may now be a live daemon's PID file.
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !os.SameFile(info, currentInfo) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func stopDaemon(path string, timeout, pollInterval time.Duration, alive func(int) bool, looksLikeTermp func(int, string) bool, signal func(int, string) error, sleep func(time.Duration), autostartWillRelaunch bool) (int, error) {
	record, info, err := readPIDIdentity(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, errDaemonNotRunning
		}
		if errors.Is(err, errPIDRecordUnparseable) {
			if removeErr := removeUnreadablePIDFile(path); removeErr != nil {
				return 0, fmt.Errorf("remove unreadable PID file: %w", removeErr)
			}
			return 0, errUnreadablePIDFileRemoved
		}
		return 0, err
	}
	pid := record.PID
	if !pidRecordIdentityMatches(record, alive, looksLikeTermp) {
		removed, removeErr := removePIDIfOwned(path, pid, info)
		if removeErr != nil {
			return 0, fmt.Errorf("remove stale PID file: %w", removeErr)
		}
		if !removed {
			return 0, errors.New("stale PID file changed before it could be removed")
		}
		return 0, errors.New("stale PID file removed; daemon is not running")
	}
	if !pidRecordIdentityMatches(record, alive, looksLikeTermp) {
		return 0, fmt.Errorf("refusing to signal pid %d: process identity changed before signaling", pid)
	}
	if err := signal(pid, record.ExecutablePath); err != nil {
		return 0, fmt.Errorf("refusing to signal pid %d: %w", pid, err)
	}
	if !waitForProcessExit(record, timeout, pollInterval, alive, looksLikeTermp, sleep) {
		return 0, fmt.Errorf("timed out after %s waiting for daemon pid %d to exit; PID file was not removed", timeout, pid)
	}
	result, err := removePIDIfOwnedResult(path, pid, info)
	if err != nil {
		return 0, fmt.Errorf("remove PID file: %w", err)
	}
	if result == pidRemovalChanged {
		if autostartWillRelaunch && pidFileOwnedByRelaunchedDaemon(path, pid, alive, looksLikeTermp) {
			return pid, nil
		}
		return 0, errors.New("daemon exited, but PID file changed ownership and was not removed")
	}
	return pid, nil
}

func stopDaemonAndPublisher(path string, publisher daemonPIDRecord, timeout, pollInterval time.Duration, alive func(int) bool, looksLikeTermp func(int, string) bool, signal func(int, string) error, sleep func(time.Duration), autostartWillRelaunch bool) (int, error) {
	record, info, readErr := readPIDIdentity(path)
	unreadable := errors.Is(readErr, errPIDRecordUnparseable)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) && !unreadable {
		return 0, readErr
	}
	if unreadable {
		if removeErr := removeUnreadablePIDFile(path); removeErr != nil {
			return 0, fmt.Errorf("remove unreadable PID file: %w", removeErr)
		}
	}
	pid := record.PID

	targets := make([]daemonPIDRecord, 0, 2)
	if processIdentityMatches(publisher.PID, publisher.StartTime, publisher.ExecutablePath, alive, looksLikeTermp) {
		targets = append(targets, publisher)
	}
	if readErr == nil && pidRecordIdentityMatches(record, alive, looksLikeTermp) && pid != publisher.PID {
		targets = append(targets, record)
	}
	if len(targets) == 0 {
		if readErr == nil {
			removed, removeErr := removePIDIfOwned(path, pid, info)
			if removeErr != nil {
				return 0, fmt.Errorf("remove stale PID file: %w", removeErr)
			}
			if !removed {
				return 0, errors.New("stale PID file changed before it could be removed")
			}
		}
		if unreadable {
			return 0, errUnreadablePIDFileRemoved
		}
		return 0, errDaemonNotRunning
	}

	for _, target := range targets {
		if !pidRecordIdentityMatches(target, alive, looksLikeTermp) {
			return 0, fmt.Errorf("refusing to signal pid %d: process identity changed before signaling", target.PID)
		}
		if err := signal(target.PID, target.ExecutablePath); err != nil {
			return 0, fmt.Errorf("refusing to signal pid %d: %w", target.PID, err)
		}
	}
	for _, target := range targets {
		if !waitForProcessExit(target, timeout, pollInterval, alive, looksLikeTermp, sleep) {
			return 0, fmt.Errorf("timed out after %s waiting for daemon pid %d to exit; PID file was not removed", timeout, target.PID)
		}
	}
	if readErr == nil {
		result, err := removePIDIfOwnedResult(path, pid, info)
		if err != nil {
			return 0, fmt.Errorf("remove PID file: %w", err)
		}
		if result == pidRemovalChanged {
			if autostartWillRelaunch && pidFileOwnedByRelaunchedDaemon(path, pid, alive, looksLikeTermp) {
				return targets[0].PID, nil
			}
			return 0, errors.New("daemon exited, but PID file changed ownership and was not removed")
		}
	}
	return targets[0].PID, nil
}

func pidFileOwnedByRelaunchedDaemon(path string, stoppedPID int, alive func(int) bool, looksLikeTermp func(int, string) bool) bool {
	record, _, err := readPIDIdentity(path)
	return err == nil && record.PID != stoppedPID &&
		pidRecordIdentityMatches(record, alive, looksLikeTermp)
}

func waitForProcessExit(record daemonPIDRecord, timeout, pollInterval time.Duration, alive func(int) bool, looksLikeTermp func(int, string) bool, sleep func(time.Duration)) bool {
	if !pidRecordIdentityMatches(record, alive, looksLikeTermp) {
		return true
	}
	if timeout <= 0 || pollInterval <= 0 {
		return false
	}
	for waited := time.Duration(0); waited < timeout; {
		delay := min(pollInterval, timeout-waited)
		sleep(delay)
		waited += delay
		if !pidRecordIdentityMatches(record, alive, looksLikeTermp) {
			return true
		}
	}
	return false
}

func isTerminal(file *os.File) bool {
	return file != nil && xterm.IsTerminal(file.Fd())
}

func maybePrintFirstRunCTA(w io.Writer, configPath string, terminal bool) {
	if !configMissing(configPath) {
		return
	}
	if !terminal {
		fmt.Fprintln(w, `First run detected — run "termp setup" to configure.`)
		return
	}

	styles := tui.DefaultCardStyles()
	body := strings.Join([]string{
		styles.Title.Render("✨  Welcome to Terminal Presence"),
		"",
		"Run:  " + styles.Accent.Render("termp setup"),
		"Discord stays blank until you do.",
	}, "\n")
	fmt.Fprintln(w, styles.Card.Render(body))
}

func configMissing(path string) bool {
	_, err := os.Stat(path)
	return isConfigMissingError(err)
}

func isConfigMissingError(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
