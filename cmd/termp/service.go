package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	xterm "github.com/charmbracelet/x/term"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
)

const maxInstallCTAWidth = 80

type autostartActionHandler func([]string) (bool, error)

type autostartManager interface {
	Install(string, bool) (service.State, error)
	Uninstall(bool) (service.State, error)
	Disable() (service.State, error)
	Enable() (service.State, error)
	Status() service.State
}

var newAutostartManager = func() autostartManager {
	return service.NewManager()
}

var stopDaemonAfterAutostart = stopRunningDaemon

func autostartActionHandlers() map[string]autostartActionHandler {
	return map[string]autostartActionHandler{
		"enable":    wrapAutostartAction(enable),
		"disable":   wrapAutostartAction(disable),
		"status":    wrapAutostartAction(status),
		"install":   wrapAutostartAction(install),
		"uninstall": uninstallAction,
	}
}

func wrapAutostartAction(handler func([]string) error) autostartActionHandler {
	return func(args []string) (bool, error) {
		return false, handler(args)
	}
}

func dispatchAutostartCommand(args []string, handlers map[string]autostartActionHandler) error {
	fs := flag.NewFlagSet("autostart", flag.ContinueOnError)
	fs.Usage = autostartUsage
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		autostartUsage()
		return flag.ErrHelp
	}
	_, err := dispatchAutostartAction(fs.Arg(0), fs.Args()[1:], handlers)
	return err
}

func dispatchAutostartAction(action string, args []string, handlers map[string]autostartActionHandler) (bool, error) {
	handler, ok := handlers[action]
	if !ok {
		autostartUsage()
		err := fmt.Errorf("%w: unknown autostart action %q", errCommandUsage, action)
		if suggestion := closestCommand(action, autostartActionNames(handlers), 2); suggestion != "" {
			return false, fmt.Errorf("%w; Did you mean %q?", err, suggestion)
		}
		return false, err
	}
	return handler(args)
}

func autostartActionNames(handlers map[string]autostartActionHandler) []string {
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func autostartUsage() {
	fmt.Fprintln(os.Stderr, "usage: termp autostart <action> [arguments]")
	fmt.Fprintln(os.Stderr, "  enable     resume login autostart")
	fmt.Fprintln(os.Stderr, "  disable    pause login autostart")
	fmt.Fprintln(os.Stderr, "  status     show daemon, Discord, autostart, and config status")
	fmt.Fprintln(os.Stderr, "  install    install the login autostart service (not the binary)")
	fmt.Fprintln(os.Stderr, "  uninstall  remove the login autostart service (not the binary)")
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	force := fs.Bool("force", false, "install from an unstable path or take over another installation's task")
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp autostart install [--force]"); err != nil {
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
	state, err := newAutostartManager().Install(exe, *force)
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if state.Message != "" {
		fmt.Printf("Warning: %s\n\n", state.Message)
	}
	fmt.Print(formatInstallSuccess(state.Path, config.DefaultPath()))
	return nil
}

func formatInstallSuccess(installedPath, configPath string) string {
	sections := []outputSection{{
		header: "Autostart",
		fields: []outputField{
			{label: "Installed", value: installedPath},
			{label: "Runs", value: "termp start"},
			{label: "Remove autostart", value: "termp autostart uninstall"},
		},
	}}
	if configMissing(configPath) {
		sections = append(sections,
			outputSection{
				header: "Config",
				fields: []outputField{{label: "Path", value: configPath}},
			},
			outputSection{
				header: "Next step",
				fields: []outputField{
					{label: "Run", value: "termp setup"},
					{label: "Why", value: "Nothing shows on your Discord profile until you do."},
				},
			},
		)
	}
	return formatSections("termp install", sections...)
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

func uninstall(args []string) error {
	_, err := uninstallAction(args)
	return err
}

type uninstallOptions struct {
	force bool
	all   bool
	yes   bool
}

func parseUninstallOptions(args []string) (uninstallOptions, error) {
	var options uninstallOptions
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.BoolVar(&options.force, "force", false, "remove a task belonging to another installation")
	fs.BoolVar(&options.all, "all", false, "remove all termp-created files and print binary removal guidance")
	fs.BoolVar(&options.yes, "yes", false, "skip confirmation when used with --all")
	if err := parseCommandFlags(fs, args); err != nil {
		return uninstallOptions{}, err
	}
	if err := rejectUnexpectedArgs(fs, "termp uninstall [--all] [--yes] [--force]"); err != nil {
		return uninstallOptions{}, err
	}
	if options.yes && !options.all {
		return uninstallOptions{}, fmt.Errorf("%w: --yes requires --all", errCommandUsage)
	}
	return options, nil
}

func uninstallAction(args []string) (bool, error) {
	options, err := parseUninstallOptions(args)
	if err != nil {
		return false, err
	}
	if options.all {
		return true, uninstallAll(options.force, options.yes)
	}
	if err := uninstallAutostart(options.force, true); err != nil {
		return false, err
	}
	return false, nil
}

func uninstallAutostart(force, stopAfter bool) error {
	manager := newAutostartManager()
	preState := manager.Status()
	wasInstalled := preState.Installed || preState.ForeignTask
	state, err := manager.Uninstall(force)
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if stopAfter {
		stoppedPID, stopped, stopErr := stopDaemonAfterAutostart()
		if stopErr != nil {
			return fmt.Errorf("autostart was removed, but the daemon could not be stopped: %w", stopErr)
		}
		if stopped {
			fmt.Printf("stopped daemon (pid %d)\n", stoppedPID)
		}
	}
	if !wasInstalled {
		fmt.Println("autostart not installed (nothing to remove)")
		return nil
	}
	path := state.Path
	if path == "" {
		path = preState.Path
	}
	fmt.Printf("removed autostart: %s (binary was not removed)\n", path)
	return nil
}

func disable(args []string) error {
	fs := flag.NewFlagSet("disable", flag.ContinueOnError)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp autostart disable"); err != nil {
		return err
	}
	state, err := newAutostartManager().Disable()
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	stoppedPID, stopped, stopErr := stopDaemonAfterAutostart()
	if stopErr != nil {
		return fmt.Errorf("autostart was disabled, but the daemon could not be stopped: %w", stopErr)
	}
	if stopped {
		fmt.Printf("stopped daemon (pid %d)\n", stoppedPID)
	}
	if !state.Installed {
		fmt.Println("autostart not installed (nothing to disable); run: termp autostart install")
		return nil
	}
	fmt.Println("disabled: autostart paused (re-enable with: termp autostart enable)")
	return nil
}

func enable(args []string) error {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, "termp autostart enable"); err != nil {
		return err
	}
	state, err := newAutostartManager().Enable()
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if !state.Installed {
		fmt.Println("autostart not installed; run: termp autostart install")
		return nil
	}
	fmt.Println("enabled: autostart active")
	return nil
}
