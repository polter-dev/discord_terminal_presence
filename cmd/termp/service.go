package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	xterm "github.com/charmbracelet/x/term"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
)

const maxInstallCTAWidth = 80

type autostartActionHandler func([]string) error

func autostartActionHandlers() map[string]autostartActionHandler {
	return map[string]autostartActionHandler{
		"enable":    enable,
		"disable":   disable,
		"status":    status,
		"install":   install,
		"uninstall": uninstall,
	}
}

func dispatchAutostartCommand(args []string, handlers map[string]autostartActionHandler) error {
	fs := flag.NewFlagSet("autostart", flag.ContinueOnError)
	fs.Usage = autostartUsage
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		autostartUsage()
		return flag.ErrHelp
	}
	return dispatchAutostartAction(fs.Arg(0), fs.Args()[1:], handlers)
}

func dispatchAutostartAction(action string, args []string, handlers map[string]autostartActionHandler) error {
	handler, ok := handlers[action]
	if !ok {
		autostartUsage()
		return fmt.Errorf("unknown autostart action %q", action)
	}
	return handler(args)
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
	if _, err := os.Stat(configPath); err != nil {
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
		fmt.Println("autostart not installed")
		return nil
	}
	fmt.Printf("removed autostart: %s (binary was not removed)\n", state.Path)
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
	if pid, info, err := readPIDRecord(pidPath); err == nil && !processAlive(pid) {
		_, _ = removePIDIfOwned(pidPath, pid, info)
	}
	fmt.Println("disabled: autostart paused (re-enable with: termp autostart enable)")
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
		fmt.Println("autostart not installed; run: termp autostart install")
		return nil
	}
	fmt.Println("enabled: autostart active")
	return nil
}
