package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("termpresence: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "install":
		err = install(os.Args[2:])
	case "uninstall":
		err = uninstall(os.Args[2:])
	case "start":
		err = start(os.Args[2:])
	case "stop":
		err = stop(os.Args[2:])
	case "status":
		err = status(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: termpresence install|uninstall|start|stop|status")
}

func start(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
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
		Pin:                  cfg.Pin,
		HeadlinerIdleTimeout: cfg.HeadlinerIdleTimeoutDuration(),
		ActivitySwitching:    cfg.ActivitySwitching,
	})
	if err != nil {
		return err
	}

	writer, err := presence.NewWriter(presence.RichClient{}, presence.DefaultAppID)
	if err != nil {
		return err
	}

	detections := det.Run(ctx)

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
					log.Print("no known terminal tool detected")
				} else {
					log.Printf("detected %s cwd=%s others=%d", detection.Tool.ID, detection.Cwd, len(detection.Others))
				}
				if !send(buildActivity(cfg, detection)) {
					return
				}
			case <-manager.Changes():
				log.Print("config reloaded")
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
	return nil
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

	// Let presence set details/image/timer; the CLI owns directory and buttons.
	detection.Others = enabledOthers(cfg, detection.Others)
	opts := presence.DisplayOptions{
		ToolName:     resolved.ToolName,
		ElapsedTimer: resolved.ElapsedTimer,
		SmallImage:   resolved.SmallImage,
		Collection:   cfg.Display.Collection,
	}
	activity, ok := presence.ActivityFromDetection(detection, opts)
	if !ok {
		return nil
	}
	if dir, show := resolved.DisplayDirectory(detection.Cwd); show {
		activity.State = dir
	}
	if resolved.ButtonsEnabled {
		activity.Buttons = presenceButtons(resolved.Buttons)
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

func presenceButtons(buttons []registry.Button) []presence.Button {
	const maxButtons = 2
	if len(buttons) > maxButtons {
		buttons = buttons[:maxButtons]
	}
	out := make([]presence.Button, 0, len(buttons))
	for _, button := range buttons {
		out = append(out, presence.Button{Label: button.Label, URL: button.URL})
	}
	return out
}

func stop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
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

func pidFilePath() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "termpresence.pid")
	}
	return filepath.Join(os.TempDir(), "termpresence.pid")
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
