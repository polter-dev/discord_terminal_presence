package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
	"github.com/polter-dev/discord_terminal_presence/internal/tui"
	usagepkg "github.com/polter-dev/discord_terminal_presence/internal/usage"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	verbose bool
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("termp: ")

	command, args, showVersion, err := parseRoot(os.Args[1:])
	if err != nil {
		usage()
		os.Exit(2)
	}
	if showVersion {
		printVersion()
		return
	}

	switch command {
	case "install":
		err = install(args)
	case "uninstall":
		err = uninstall(args)
	case "start":
		err = start(args)
	case "stop":
		err = stop(args)
	case "status":
		err = status(args)
	case "settings":
		err = settings(args)
	case "version":
		err = versionCommand(args)
	case "setup":
		err = setup(args)
	case "config":
		err = configCommand(args)
	case "completion":
		err = completion(args)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: termp [--verbose] [--version] install|uninstall|start|stop|status|settings|version|setup|config|completion")
}

func parseRoot(args []string) (command string, commandArgs []string, showVersion bool, err error) {
	fs := flag.NewFlagSet("termp", flag.ContinueOnError)
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	fs.BoolVar(&verbose, "v", false, "enable verbose logging")
	fs.BoolVar(&showVersion, "version", false, "print version information")
	if err := fs.Parse(args); err != nil {
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
		log.Printf(format, args...)
	}
}

func versionCommand(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	printVersion()
	return nil
}

func printVersion() {
	fmt.Print(formatVersion())
}

func formatVersion() string {
	return fmt.Sprintf("termp %s (%s, %s)\ngo %s\n%s/%s\n",
		version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func configCommand(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: termp config init [--force]")
		return flag.ErrHelp
	}
	switch args[0] {
	case "init":
		return configInit(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: termp config init [--force]")
		return flag.ErrHelp
	}
}

func configInit(args []string) error {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	addVerboseFlag(fs)
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := config.DefaultPath()
	if err := config.InitFile(path, *force); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	save := func(cfg config.Config) (string, error) {
		path := config.DefaultPath()
		return path, config.Save(cfg, path)
	}
	installAutostart := func(exe string) error {
		_, err := service.NewManager().Install(exe)
		return err
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		path, err := save(config.Default())
		if err != nil {
			return err
		}
		fmt.Printf("Wrote default config: %s\n", path)
		fmt.Println("Non-interactive setup skipped autostart. Run `termp install` to enable autostart, then `termp start` to run now.")
		return nil
	}
	model := tui.NewSetupModel(save, installAutostart, service.ResolveExecutable)
	finalModel, err := tea.NewProgram(model).Run()
	if err != nil {
		return err
	}
	if setupModel, ok := finalModel.(tui.SetupModel); ok && setupModel.Err() != nil {
		return setupModel.Err()
	}
	return nil
}

func completion(args []string) error {
	fs := flag.NewFlagSet("completion", flag.ContinueOnError)
	addVerboseFlag(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: termp completion <bash|zsh|fish>")
		fmt.Fprintln(os.Stderr, "bash: source <(termp completion bash)")
		fmt.Fprintln(os.Stderr, "zsh:  termp completion zsh > ${fpath[1]}/_termp")
		fmt.Fprintln(os.Stderr, "fish: termp completion fish > ~/.config/fish/completions/termp.fish")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	script, err := completionScript(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(script)
	return nil
}

func completionScript(shell string) (string, error) {
	commands := "start stop status install uninstall settings version setup completion"
	switch shell {
	case "bash":
		return `_termp_complete() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [[ ${COMP_CWORD} -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "--verbose -v --version ` + commands + `" -- "$cur") )
  fi
}
complete -F _termp_complete termp
`, nil
	case "zsh":
		return `#compdef termp
_arguments \
  '(-v --verbose)'{-v,--verbose}'[enable verbose logging]' \
  '--version[print version information]' \
  '1:command:(` + commands + `)' \
  '*::arg:->args'
`, nil
	case "fish":
		var b strings.Builder
		b.WriteString("complete -c termp -f\n")
		b.WriteString("complete -c termp -s v -l verbose -d 'enable verbose logging'\n")
		b.WriteString("complete -c termp -l version -d 'print version information'\n")
		for _, command := range strings.Fields(commands) {
			b.WriteString(fmt.Sprintf("complete -c termp -n '__fish_use_subcommand' -a %s\n", command))
		}
		return b.String(), nil
	default:
		return "", fmt.Errorf("unsupported shell %q (want bash, zsh, or fish)", shell)
	}
}

func start(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	pidPath := pidFilePath()
	if pid, err := readPID(pidPath); err == nil && processAlive(pid) {
		return fmt.Errorf("daemon already running with pid %d", pid)
	}
	if err := writePID(pidPath, os.Getpid()); err != nil {
		return err
	}
	defer removePID(pidPath)

	manager := config.NewManager()
	cfg, loadErr := manager.Current()
	if loadErr != nil {
		log.Printf("config load error, using last-good/default config: %v", loadErr)
	}
	for _, warning := range cfg.Warnings {
		log.Print(warning)
	}

	if err := config.EnsureConfigDir(cfg.Path); err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := manager.Watch(ctx); err != nil {
			log.Printf("config watch disabled: %v", err)
		}
		return run(ctx, manager)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return run(ctx, manager)
}

func run(ctx context.Context, manager *config.Manager) error {
	cfg, _ := manager.Current()
	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return err
	}
	det, err := detector.New(reg, detector.GopsutilLister{}, detector.Config{
		ScanInterval:         cfg.ScanIntervalDuration(),
		IdleClearTimeout:     cfg.IdleClearTimeoutDuration(),
		Pin:                  cfg.Pin,
		HeadlinerIdleTimeout: cfg.HeadlinerIdleTimeoutDuration(),
		ActivitySwitching:    cfg.ActivitySwitching,
	})
	if err != nil {
		return err
	}

	writerOptions := []presence.WriterOption{}
	if verbose {
		writerOptions = append(writerOptions, presence.WithDebugf(debugf))
	}
	writer, err := presence.NewWriter(presence.RichClient{}, presence.DefaultAppID, writerOptions...)
	if err != nil {
		return err
	}

	detections := det.Run(ctx)
	usagePath := usagepkg.StatePath()
	usageStore, err := usagepkg.Load(usagePath)
	if err != nil {
		debugf("usage load skipped: %v", err)
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

	// Translate detector events into config-resolved activities. A config reload
	// re-applies the current detection so toggles take effect without waiting for
	// the active tool to change.
	activities := make(chan *presence.Activity)
	go func() {
		defer close(activities)
		var (
			last     detector.Detection
			haveLast bool
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
				cfg, _ := manager.Current()
				if detection.None {
					debugf("scan result: none")
				} else {
					debugf("scan result: featured=%s cwd=%s others=%s", detection.Tool.ID, detection.Cwd, otherToolIDs(detection.Others))
					recordUsage(usageStore, detection, time.Now())
					saveUsage(false)
				}
				if !send(buildActivity(cfg, detection)) {
					return
				}
			case <-manager.Changes():
				debugf("config reloaded")
				if haveLast {
					cfg, _ := manager.Current()
					if !send(buildActivity(cfg, last)) {
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	writer.RunActivities(ctx, activities)
	saveUsage(true)
	return nil
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

// buildActivity resolves the config for a detection and produces the presence
// activity to display, or nil to clear presence. The directory (state) and
// buttons are decided here so the privacy allowlist and per-tool button
// overrides in internal/config are honored; internal/presence stays
// config-agnostic.
func buildActivity(cfg config.Config, detection detector.Detection) *presence.Activity {
	if detection.None {
		return nil
	}
	resolved := cfg.Resolve(detection.Tool)
	if !resolved.Enabled {
		return nil
	}

	displayDir, showDir := resolved.DisplayDirectory(detection.Cwd)
	detection.Others = enabledOthers(cfg, detection.Others)
	if showDir {
		detection.Cwd = displayDir
	} else {
		detection.Cwd = ""
	}
	opts := presence.DisplayOptions{
		ToolName:              resolved.ToolName,
		DetailsFormat:         cfg.DetailsFormat,
		ElapsedTimer:          resolved.ElapsedTimer,
		SmallImage:            resolved.SmallImage,
		Collection:            cfg.Display.Collection,
		ShowDirectory:         showDir,
		DirectoryBasenameOnly: false,
	}
	activity, ok := presence.ActivityFromDetection(detection, opts)
	if !ok {
		return nil
	}
	if resolved.ButtonsEnabled {
		activity.Buttons = activityButtons(resolved.Buttons, cfg.CTA)
	}
	return &activity
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	pidPath := pidFilePath()
	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("daemon is not running")
		}
		return err
	}
	if !processAlive(pid) {
		removePID(pidPath)
		return errors.New("stale PID file removed; daemon is not running")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	removePID(pidPath)
	fmt.Println("stopped")
	return nil
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, loadErr := config.Load()
	pidPath := pidFilePath()
	running := false
	if pid, err := readPID(pidPath); err == nil {
		running = processAlive(pid)
	}

	fmt.Printf("running: %t\n", running)
	if err := presence.Probe(presence.DefaultAppID); err != nil {
		fmt.Println("discord: not running (start Discord to show presence)")
	} else {
		fmt.Println("discord: connected")
	}
	serviceState := service.NewManager().Status()
	fmt.Printf("service_supported: %t\n", serviceState.Supported)
	if serviceState.Path != "" {
		fmt.Printf("service_path: %s\n", serviceState.Path)
	}
	fmt.Printf("service_installed: %t\n", serviceState.Installed)
	if serviceState.Loaded != "" {
		fmt.Printf("service_loaded: %s\n", serviceState.Loaded)
	}
	if serviceState.Enabled != "" {
		fmt.Printf("service_enabled: %s\n", serviceState.Enabled)
	}
	if serviceState.Message != "" {
		fmt.Printf("service_message: %s\n", serviceState.Message)
	}
	fmt.Printf("config_path: %s\n", cfg.Path)
	if loadErr != nil {
		fmt.Printf("config_ok: false\nconfig_error: %v\n", loadErr)
	} else {
		fmt.Println("config_ok: true")
		for _, warning := range cfg.Warnings {
			fmt.Printf("config_warning: %s\n", warning)
		}
	}

	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return err
	}
	processes, err := detector.GopsutilLister{}.List()
	if err != nil {
		fmt.Printf("detected_tool: unknown (%v)\n", err)
		return nil
	}
	detection := detector.ActiveDetection(reg, processes)
	if detection.None {
		fmt.Println("detected_tool: none")
		return nil
	}
	fmt.Printf("detected_tool: %s\n", detection.Tool.ID)
	return nil
}

func settings(args []string) error {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	addVerboseFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr, "termp settings requires an interactive terminal (TTY)")
		return errors.New("settings requires a TTY")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return err
	}
	usageStore, err := usagepkg.Load(usagepkg.StatePath())
	if err != nil {
		debugf("usage load skipped: %v", err)
		usageStore = usagepkg.New()
	}
	model := tui.NewSettingsModel(cfg, reg.Tools(), usageStore.Rank(), func(next config.Config) error {
		return config.Save(next, config.DefaultPath())
	}, openInBrowser)
	_, err = tea.NewProgram(model).Run()
	return err
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
	return filepath.Join(os.TempDir(), "termp.pid")
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func writePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func removePID(path string) {
	_ = os.Remove(path)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
